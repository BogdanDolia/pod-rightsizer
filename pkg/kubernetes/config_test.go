package kubernetes

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewClientUsesKubeconfigEnvironment(t *testing.T) {
	clearInClusterEnvironment(t)
	server := configTestServer(t)
	path := writeConfigTestFile(t, "config", completeTestConfig(server))
	t.Setenv("KUBECONFIG", path)
	client, err := NewClient("")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	checkConfigTestClients(t, client)
}

func TestNewClientExplicitPathOverridesKubeconfigEnvironment(t *testing.T) {
	server := configTestServer(t)
	path := writeConfigTestFile(t, "explicit", completeTestConfig(server))
	t.Setenv("KUBECONFIG", writeConfigTestFile(t, "broken", "not: [valid yaml"))
	client, err := NewClient(path)
	if err != nil {
		t.Fatalf("NewClient(explicit) error = %v", err)
	}
	checkConfigTestClients(t, client)
}

func TestNewClientMergesKubeconfigEnvironmentPathList(t *testing.T) {
	clearInClusterEnvironment(t)
	server := configTestServer(t)
	cluster := writeConfigTestFile(t, "cluster", fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: demo
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: demo-user
  user:
    token: test-only-token
`, server.URL, configTestCA(server)))
	selection := writeConfigTestFile(t, "selection", `apiVersion: v1
kind: Config
contexts:
- name: demo-context
  context:
    cluster: demo
    user: demo-user
current-context: demo-context
`)
	t.Setenv("KUBECONFIG", strings.Join([]string{cluster, selection}, string(os.PathListSeparator)))
	client, err := NewClient("")
	if err != nil {
		t.Fatalf("NewClient(merged) error = %v", err)
	}
	checkConfigTestClients(t, client)
}

func TestNewClientRejectsMalformedEnvironmentConfig(t *testing.T) {
	clearInClusterEnvironment(t)
	path := writeConfigTestFile(t, "broken", "not: [valid yaml")
	t.Setenv("KUBECONFIG", path)
	_, err := NewClient("")
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("NewClient() error = %v, want malformed selected config error", err)
	}
}

func clearInClusterEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
}

func completeTestConfig(server *httptest.Server) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: demo
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: demo-user
  user:
    token: test-only-token
contexts:
- name: demo-context
  context:
    cluster: demo
    user: demo-user
current-context: demo-context
`, server.URL, configTestCA(server))
}

func configTestCA(server *httptest.Server) string {
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	return base64.StdEncoding.EncodeToString(certificate)
}

func writeConfigTestFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func configTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-only-token" {
			http.Error(w, "wrong config credentials", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/namespaces":
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"NamespaceList","items":[]}`)
		case "/apis/metrics.k8s.io/v1beta1/namespaces/demo/pods":
			fmt.Fprint(w, `{"apiVersion":"metrics.k8s.io/v1beta1","kind":"PodMetricsList","items":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func checkConfigTestClients(t *testing.T, client *Client) {
	t.Helper()
	if _, err := client.clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatalf("core client did not use selected config: %v", err)
	}
	if _, err := client.metricsClient.MetricsV1beta1().PodMetricses("demo").List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatalf("metrics client did not use selected config: %v", err)
	}
}
