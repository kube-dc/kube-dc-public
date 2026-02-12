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

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Kube-DC E2E Test Suite")
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
	Expect(kubeovn.AddToScheme(testScheme)).To(Succeed())
	Expect(netattachdef.AddToScheme(testScheme)).To(Succeed())
	Expect(hncv1alpha2.AddToScheme(testScheme)).To(Succeed())

	k8sClient, err = client.New(cfg, client.Options{Scheme: testScheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

})
