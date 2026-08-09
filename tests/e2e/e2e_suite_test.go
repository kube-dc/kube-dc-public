package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	netattachdef "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	kubeovn "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kubedccomv1 "github.com/shalb/kube-dc/api/kube-dc.com/v1"
	securityv1alpha1 "github.com/shalb/kube-dc/api/security.kube-dc.com/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	hncv1alpha2 "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

var (
	k8sClient  client.Client
	cfg        *rest.Config
	testScheme = runtime.NewScheme()
	ctx        context.Context
)

// Logf is a helper function for verbose logging in e2e tests.
// It prints logs to the GinkgoWriter, which is safe for parallel tests.
// Logging can be disabled by setting the E2E_VERBOSE_LOGS environment variable to "false".
func Logf(format string, a ...interface{}) {
	if os.Getenv("E2E_VERBOSE_LOGS") == "false" {
		return
	}
	fmt.Fprintf(GinkgoWriter, time.Now().Format("2006-01-02 15:04:05")+": "+format+"\n", a...)
}

// E2EOptInEnv must be set for this suite to run at all.
//
// These specs are NOT hermetic: BeforeSuite calls config.GetConfig(), which
// silently picks up the ambient kubeconfig, and the specs then CREATE AND
// DELETE Organizations, Projects and namespaces on whatever cluster that
// happens to point at. There is no envtest here and no dry-run mode.
//
// `make test` avoids them only by filtering the package list
// (`go list ./... | grep -v /e2e`). Nothing protected a plain
// `go test ./...`, `go test -race ./...`, an IDE "run all tests" action, or a
// CI job that does not know about the Makefile — each of those would have run
// destructive specs against the operator's current kubecontext, which on this
// team is routinely a live production cluster. A reviewer hit exactly that on
// 2026-07-20 and had to interrupt the run.
//
// The guard is an explicit opt-in rather than a build tag so the package still
// compiles and vets in normal runs — a build tag would have hidden the code
// from `go vet ./...` and from refactoring tools.
const E2EOptInEnv = "KUBE_DC_E2E"

func TestE2E(t *testing.T) {
	if os.Getenv(E2EOptInEnv) == "" {
		t.Skipf("skipping live-cluster E2E: set %s=1 to run. "+
			"These specs create and delete real Organizations, Projects and namespaces "+
			"on the cluster your current kubecontext points at (%s).",
			E2EOptInEnv, currentContextHint())
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "Kube-DC E2E Test Suite")
}

// currentContextHint names the cluster that WOULD have been targeted, so the
// skip message is actionable instead of abstract. Best-effort and never fatal.
func currentContextHint() string {
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		return "KUBECONFIG=" + kc
	}
	return "default kubeconfig ~/.kube/config"
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx = context.Background()

	By("bootstrapping test environment")
	var err error
	cfg, err = config.GetConfig()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	// Register the CRD scheme
	Expect(scheme.AddToScheme(testScheme)).To(Succeed())
	Expect(kubedccomv1.AddToScheme(testScheme)).To(Succeed())
	Expect(securityv1alpha1.AddToScheme(testScheme)).To(Succeed())
	Expect(kubeovn.AddToScheme(testScheme)).To(Succeed())
	Expect(netattachdef.AddToScheme(testScheme)).To(Succeed())
	Expect(hncv1alpha2.AddToScheme(testScheme)).To(Succeed())

	k8sClient, err = client.New(cfg, client.Options{Scheme: testScheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// M1-T12b tenant fixture (Keycloak test users + JWTs). Lives in a
	// separate file so the Organization/Project/Workload suites stay
	// unaffected; the setup function records its own Skip reason so
	// the global BeforeSuite never fails because of a missing
	// realm-access secret on a fresh cluster.
	setupT12bTenant()
})

var _ = AfterSuite(func() {
	teardownT12bTenant()
})
