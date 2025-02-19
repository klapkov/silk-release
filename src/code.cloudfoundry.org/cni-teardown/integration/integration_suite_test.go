package integration_test

import (
	"encoding/json"
	"fmt"
	"math/rand"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"os"
	"path/filepath"
	"testing"
)

var (
	paths testPaths
)

type testPaths struct {
	TeardownBin string
}

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Teardown Integration Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error

	fmt.Fprintf(GinkgoWriter, "building binary...")
	paths.TeardownBin, err = gexec.Build("code.cloudfoundry.org/cni-teardown", "-buildvcs=false")
	fmt.Fprintf(GinkgoWriter, "done")
	Expect(err).NotTo(HaveOccurred())

	err = os.Chmod(paths.TeardownBin, 0777)
	Expect(err).NotTo(HaveOccurred())

	dir := filepath.Dir(paths.TeardownBin)
	for dir != filepath.Dir(dir) {
		Expect(os.Chmod(dir, 0777)).To(Succeed())
		dir = filepath.Dir(dir)
	}

	data, err := json.Marshal(paths)
	Expect(err).NotTo(HaveOccurred())

	return data
}, func(data []byte) {
	Expect(json.Unmarshal(data, &paths)).To(Succeed())
	suiteConfig, _ := GinkgoConfiguration()
	rand.Seed(suiteConfig.RandomSeed + int64(GinkgoParallelProcess()))
})

var _ = SynchronizedAfterSuite(func() {}, func() {
	gexec.CleanupBuildArtifacts()
})
