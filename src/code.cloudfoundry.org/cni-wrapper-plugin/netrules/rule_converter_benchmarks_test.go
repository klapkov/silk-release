package netrules_test

import (
	"bytes"
	"fmt"
	"testing"

	"code.cloudfoundry.org/cni-wrapper-plugin/netrules"
	"code.cloudfoundry.org/lib/rules"
)

func generateRules() []rules.IPTablesRule {
	var ipRules []rules.IPTablesRule

	for i := 0; i < 10000; i++ {
		dstPort := "2020:2020"

		if i%3 == 0 {
			dstPort = fmt.Sprintf("%d:%d", i, i)
		}

		rule := rules.IPTablesRule{
			"-m", "iprange", "-p", "tcp",
			"--dst-range", "1.1.1-2.2.2.2",
			"-m", "tcp", "--destination-port", dstPort,
			"--jump", "ACCEPT",
		}

		ipRules = append(ipRules, rule)
	}

	return ipRules
}

func BenchmarkDeduplicateRules(b *testing.B) {
	converter := &netrules.RuleConverter{LogWriter: &bytes.Buffer{}}
	unfilteredRules := generateRules()

	b.Run("original", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			deduped := converter.DeduplicateRules(unfilteredRules)

			if len(deduped) == 0 {
				b.Fatal("Deduped rules should not be empty")
			}
		}
	})
}
