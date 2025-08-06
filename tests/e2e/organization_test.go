package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"fmt"
	"github.com/Nerzal/gocloak/v13"
	kubedccomv1 "github.com/shalb/kube-dc/api/kube-dc.com/v1"
	corev1 "k8s.io/api/core/v1"
	"net/url"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	defaultKeycloakOrgClientName       = "kube-dc"
	defaultKeycloakOrgAdminGroup       = "org-admin"
	defaultKeycloakAccessTokenLifespan = 300
	defaultKeycloakLang                = "en"
)

var _ = Describe("Organization", func() {

	It("Should create an Organization and dependent resources successfully", func() {
		orgName := "test-org-e2e"

		Logf("BEGIN: Organization test for '%s'", orgName)
		By("Creating a Namespace for the Organization: " + orgName)
		orgNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: orgName,
			},
		}
		Expect(k8sClient.Create(ctx, orgNamespace)).Should(Succeed())
		Logf("SUCCESS: Created namespace '%s'", orgName)

		// Defer cleanup for the namespace
		defer func() {
			By("Deleting the Organization's Namespace")
			Expect(k8sClient.Delete(ctx, orgNamespace)).Should(Succeed())
			Logf("SUCCESS: Deleted namespace '%s'", orgName)
		}()

		By("Creating a new Organization: " + orgName)
		org := &kubedccomv1.Organization{
			ObjectMeta: metav1.ObjectMeta{
				Name:      orgName,
				Namespace: orgName, // Create the org inside its own namespace
			},
			Spec: kubedccomv1.OrganizationSpec{
				Description: func() *string { s := "E2E Test Organization"; return &s }(),
				Email:       func() *string { s := "test@example.com"; return &s }(),
			},
		}
		Expect(k8sClient.Create(ctx, org)).Should(Succeed())
		Logf("SUCCESS: Created Organization '%s' in namespace '%s'", org.Name, org.Namespace)

		// Defer cleanup for the organization
		defer func() {
			By("Deleting the Organization")
			Expect(k8sClient.Delete(ctx, org)).Should(Succeed())
			Logf("SUCCESS: Deleted Organization '%s'", org.Name)
		}()

		By("Waiting for the Organization to become Ready")
		createdOrg := &kubedccomv1.Organization{}
		orgKey := types.NamespacedName{Name: orgName, Namespace: orgName}
		Eventually(func() bool {
			err := k8sClient.Get(ctx, orgKey, createdOrg)
			if err != nil {
				return false
			}
			return createdOrg.Status.Ready
		}, time.Minute*2, time.Second*5).Should(BeTrue(), "The organization should become ready after its namespace is created")
		Logf("SUCCESS: Organization '%s' is Ready", orgName)

		By("Verifying that the Keycloak realm access secret was created")
		realmSecret := &corev1.Secret{}
		secretKey := types.NamespacedName{Name: "realm-access", Namespace: orgName}
		Eventually(func() error {
			return k8sClient.Get(ctx, secretKey, realmSecret)
		}, time.Minute*1, time.Second*2).Should(Succeed(), "The realm-access secret should be created by the controller")
		Logf("SUCCESS: Found 'realm-access' secret in namespace '%s'", orgName)

		Expect(realmSecret.Data).Should(HaveKey("url"), "The secret should contain a 'url' key")
		Expect(realmSecret.Data).Should(HaveKey("password"), "The secret should contain a 'password' key")
		Expect(realmSecret.Data).Should(HaveKey("user"), "The secret should contain a 'user' key")
		Logf("SUCCESS: 'realm-access' secret contains expected keys")

		By("Authenticating with Keycloak using the realm-access credentials")
		consoleURL := string(realmSecret.Data["url"])
		adminUsername := string(realmSecret.Data["user"])
		adminPassword := string(realmSecret.Data["password"])

		Expect(consoleURL).NotTo(BeEmpty())
		Expect(adminUsername).NotTo(BeEmpty())
		Expect(adminPassword).NotTo(BeEmpty())

		// The URL in the secret is the console URL, but gocloak needs the base server URL.
		parsedURL, err := url.Parse(consoleURL)
		Expect(err).NotTo(HaveOccurred())
		keycloakBaseURL := fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)
		Logf("Parsed Keycloak base URL: %s", keycloakBaseURL)

		gcClient := gocloak.NewClient(keycloakBaseURL)
		token, err := gcClient.LoginAdmin(ctx, adminUsername, adminPassword, orgName)
		Expect(err).NotTo(HaveOccurred(), "Should be able to login to Keycloak with the provided credentials")
		Expect(token).NotTo(BeNil())
		Expect(token.AccessToken).NotTo(BeEmpty())
		Logf("SUCCESS: Successfully authenticated with Keycloak as admin for realm '%s'", orgName)

		By("Verifying the Keycloak realm configuration")
		// Get the realm
		realm, err := gcClient.GetRealm(ctx, token.AccessToken, orgName)
		Expect(err).NotTo(HaveOccurred())
		Expect(*realm.DisplayName).To(Equal(orgName))
		Expect(*realm.Enabled).To(BeTrue())
		Expect(*realm.AccessTokenLifespan).To(Equal(defaultKeycloakAccessTokenLifespan))
		Expect(*realm.DefaultLocale).To(Equal(defaultKeycloakLang))
		Logf("SUCCESS: Realm '%s' is configured correctly", orgName)

		// Get the client
		clients, err := gcClient.GetClients(ctx, token.AccessToken, orgName, gocloak.GetClientsParams{ClientID: gocloak.StringP(defaultKeycloakOrgClientName)})
		Expect(err).NotTo(HaveOccurred())
		Expect(clients).To(HaveLen(1), "Expected to find one client with ID '%s'", defaultKeycloakOrgClientName)
		Expect(*clients[0].Enabled).To(BeTrue())
		Logf("SUCCESS: Client '%s' is configured correctly", defaultKeycloakOrgClientName)

		// Get the admin user
		users, err := gcClient.GetUsers(ctx, token.AccessToken, orgName, gocloak.GetUsersParams{Username: &adminUsername})
		Expect(err).NotTo(HaveOccurred())
		Expect(users).To(HaveLen(1), "Expected to find one user with username '%s'", adminUsername)
		adminUser := users[0]
		Expect(*adminUser.Enabled).To(BeTrue())
		By("Verifying the admin user's email")
		Expect(*adminUser.Email).To(Equal(*org.Spec.Email))
		Logf("SUCCESS: Admin user '%s' is configured correctly", adminUsername)

		// Get the user's groups
		userGroups, err := gcClient.GetUserGroups(ctx, token.AccessToken, orgName, *adminUser.ID, gocloak.GetGroupsParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(userGroups).To(HaveLen(1), "Expected admin user to be in one group")
		Expect(*userGroups[0].Name).To(Equal(defaultKeycloakOrgAdminGroup))
		Logf("SUCCESS: Admin user '%s' is in group '%s'", adminUsername, defaultKeycloakOrgAdminGroup)

		Logf("END: Organization test for '%s'", orgName)
	})
})
