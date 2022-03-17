package converger

import (
	"fmt"
	"regexp"
	"time"

	"code.cloudfoundry.org/vxlan-policy-agent/enforcer"
	"code.cloudfoundry.org/vxlan-policy-agent/planner"
	"github.com/hashicorp/go-multierror"

	"sync"

	"code.cloudfoundry.org/lager"
)

//go:generate counterfeiter -o fakes/planner.go --fake-name Planner . Planner
type Planner interface {
	GetPolicyRulesAndChain() (enforcer.RulesWithChain, error)
	GetASGRulesAndChains(containers ...string) ([]enforcer.RulesWithChain, error)
}

//go:generate counterfeiter -o fakes/rule_enforcer.go --fake-name RuleEnforcer . ruleEnforcer
type ruleEnforcer interface {
	EnforceRulesAndChain(enforcer.RulesWithChain) (string, error)
	CleanChainsMatching(regex *regexp.Regexp, desiredChains []enforcer.LiveChain) ([]enforcer.LiveChain, error)
}

//go:generate counterfeiter -o fakes/metrics_sender.go --fake-name MetricsSender . metricsSender
type metricsSender interface {
	SendDuration(string, time.Duration)
}

type SinglePollCycle struct {
	planners            []Planner
	enforcer            ruleEnforcer
	metricsSender       metricsSender
	logger              lager.Logger
	policyRuleSets      map[enforcer.Chain]enforcer.RulesWithChain
	asgRuleSets         map[enforcer.LiveChain]enforcer.RulesWithChain
	containerToASGChain map[enforcer.LiveChain]string
	policyMutex         sync.Locker
	asgMutex            sync.Locker
}

func NewSinglePollCycle(planners []Planner, re ruleEnforcer, ms metricsSender, logger lager.Logger) *SinglePollCycle {
	return &SinglePollCycle{
		planners:      planners,
		enforcer:      re,
		metricsSender: ms,
		logger:        logger,
		policyMutex:   new(sync.Mutex),
		asgMutex:      new(sync.Mutex),
	}
}

const metricEnforceDuration = "iptablesEnforceTime"
const metricPollDuration = "totalPollTime"

const metricASGEnforceDuration = "asgIptablesEnforceTime"
const metricASGCleanupDuration = "asgIptablesCleanupTime"
const metricASGPollDuration = "asgTotalPollTime"

func (m *SinglePollCycle) DoPolicyCycle() error {
	m.policyMutex.Lock()

	if m.policyRuleSets == nil {
		m.policyRuleSets = make(map[enforcer.Chain]enforcer.RulesWithChain)
	}

	pollStartTime := time.Now()
	var enforceDuration time.Duration
	for _, p := range m.planners {
		ruleSet, err := p.GetPolicyRulesAndChain()
		if err != nil {
			m.policyMutex.Unlock()
			return fmt.Errorf("get-rules: %s", err)
		}
		enforceStartTime := time.Now()

		oldRuleSet := m.policyRuleSets[ruleSet.Chain]
		if !ruleSet.Equals(oldRuleSet) {
			m.logger.Debug("poll-cycle", lager.Data{
				"message":       "updating iptables rules",
				"num old rules": len(oldRuleSet.Rules),
				"num new rules": len(ruleSet.Rules),
				"old rules":     oldRuleSet,
				"new rules":     ruleSet,
			})
			_, err = m.enforcer.EnforceRulesAndChain(ruleSet)
			if err != nil {
				m.policyMutex.Unlock()
				return fmt.Errorf("enforce: %s", err)
			}
			m.policyRuleSets[ruleSet.Chain] = ruleSet
		}

		enforceDuration += time.Now().Sub(enforceStartTime)
	}

	m.policyMutex.Unlock()

	pollDuration := time.Now().Sub(pollStartTime)
	m.metricsSender.SendDuration(metricEnforceDuration, enforceDuration)
	m.metricsSender.SendDuration(metricPollDuration, pollDuration)

	return nil
}

func (m *SinglePollCycle) DoASGCycle() error {
	return m.SyncASGsForContainers() // syncs for all containers when arguments are empty
}

func (m *SinglePollCycle) SyncASGsForContainers(containers ...string) error {
	m.asgMutex.Lock()

	if m.asgRuleSets == nil {
		m.asgRuleSets = make(map[enforcer.LiveChain]enforcer.RulesWithChain)
	}
	if m.containerToASGChain == nil {
		m.containerToASGChain = make(map[enforcer.LiveChain]string)
	}

	pollStartTime := time.Now()
	var enforceDuration time.Duration

	var allRuleSets []enforcer.RulesWithChain
	var desiredChains []enforcer.LiveChain

	var errors error

	for _, p := range m.planners {
		asgrulesets, err := p.GetASGRulesAndChains(containers...)
		if err != nil {
			m.asgMutex.Unlock()
			return fmt.Errorf("get-asg-rules: %s", err)
		}

		enforceStartTime := time.Now()

		allRuleSets = append(allRuleSets, asgrulesets...)
		for _, ruleset := range asgrulesets {
			chainKey := enforcer.LiveChain{Table: ruleset.Chain.Table, Name: ruleset.Chain.ParentChain}
			oldRuleSet := m.asgRuleSets[chainKey]
			if !ruleset.Equals(oldRuleSet) {
				m.logger.Debug("poll-cycle-asg", lager.Data{
					"message":       "updating iptables rules",
					"num old rules": len(oldRuleSet.Rules),
					"num new rules": len(ruleset.Rules),
					"old rules":     oldRuleSet,
					"new rules":     ruleset,
				})
				chain, err := m.enforcer.EnforceRulesAndChain(ruleset)
				if err != nil {
					errors = multierror.Append(errors, fmt.Errorf("enforce-asg: %s", err))
				} else {
					// only overwrite the container/rule caches if we did not error here
					m.containerToASGChain[chainKey] = chain
					m.asgRuleSets[chainKey] = ruleset
				}
			}
			desiredChains = append(desiredChains, enforcer.LiveChain{Table: ruleset.Chain.Table, Name: m.containerToASGChain[chainKey]})
		}
		enforceDuration += time.Now().Sub(enforceStartTime)
	}

	pollingLoop := len(containers) == 0

	var cleanupDuration time.Duration
	if pollingLoop {
		cleanupStart := time.Now()
		err := m.cleanupASGsChains(planner.ASGManagedChainsRegex, desiredChains)
		if err != nil {
			errors = multierror.Append(errors, err)
		}
		cleanupDuration = time.Now().Sub(cleanupStart)
	}
	m.asgMutex.Unlock()

	if pollingLoop {
		m.metricsSender.SendDuration(metricASGEnforceDuration, enforceDuration)
		m.metricsSender.SendDuration(metricASGCleanupDuration, cleanupDuration)
		pollDuration := time.Now().Sub(pollStartTime)
		m.metricsSender.SendDuration(metricASGPollDuration, pollDuration)
	}

	return errors
}

func (m *SinglePollCycle) CleanupOrphanedASGsChains(containerHandle string) error {
	m.asgMutex.Lock()
	defer m.asgMutex.Unlock()

	return m.cleanupASGsChains(planner.ASGChainPrefix(containerHandle), []enforcer.LiveChain{})
}

func (m *SinglePollCycle) cleanupASGsChains(prefix string, desiredChains []enforcer.LiveChain) error {
	deletedChains, err := m.enforcer.CleanChainsMatching(regexp.MustCompile(prefix), desiredChains)
	if err != nil {
		return fmt.Errorf("clean-up-orphaned-asg-chains: %s", err)
	} else {
		m.logger.Debug("policy-cycle-asg", lager.Data{
			"message": "deleted-orphaned-chains",
			"chains":  deletedChains,
		})

		for chainKey, chainName := range m.containerToASGChain {
			for _, deletedChain := range deletedChains {
				if deletedChain.Table == chainKey.Table && deletedChain.Name == chainName {
					delete(m.containerToASGChain, chainKey)
					delete(m.asgRuleSets, chainKey)
				}
			}
		}
	}

	return nil
}

// used to test that we're deleting the right chains and nothing else
func (m *SinglePollCycle) CurrentlyAppliedChainNames() []string {
	chains := []string{}
	for _, chain := range m.containerToASGChain {
		chains = append(chains, chain)
	}
	return chains
}
