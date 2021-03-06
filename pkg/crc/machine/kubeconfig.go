package machine

import (
	gocontext "context"
	"crypto/tls"
	"crypto/x509"
	goerrors "errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/code-ready/crc/pkg/crc/cluster"
	"github.com/code-ready/crc/pkg/crc/constants"
	"github.com/code-ready/crc/pkg/crc/errors"
	"github.com/code-ready/crc/pkg/crc/logging"
	"github.com/code-ready/crc/pkg/crc/oc"
	"github.com/openshift/oc/pkg/helpers/tokencmd"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

const (
	adminContext     = "crc-admin"
	developerContext = "crc-developer"
)

func eventuallyWriteKubeconfig(ocConfig oc.Config, ip string, clusterConfig *ClusterConfig) error {
	if err := errors.RetryAfter(60, func() error {
		status, err := cluster.GetClusterOperatorStatus(ocConfig, "authentication")
		if err != nil {
			return &errors.RetriableError{Err: err}
		}
		if isReady(status) {
			return nil
		}
		return &errors.RetriableError{Err: goerrors.New("cluster operator authentication not ready")}
	}, 2*time.Second); err != nil {
		logging.Warn("Skipping the kubeconfig update. Cluster operator authentication still not ready after 2min.")
	} else if err := WriteKubeconfig(ip, clusterConfig); err != nil {
		return err
	}
	return nil
}

func isReady(status *cluster.Status) bool {
	return status.Available && !status.Progressing && !status.Degraded && !status.Disabled
}

func WriteKubeconfig(ip string, clusterConfig *ClusterConfig) error {
	kubeconfig := getGlobalKubeConfigPath()
	dir := filepath.Dir(kubeconfig)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// Make sure .kube/config exist if not then this will create
	_, _ = os.OpenFile(kubeconfig, os.O_RDONLY|os.O_CREATE, 0600)

	ca, err := certificateAuthority(clusterConfig)
	if err != nil {
		return err
	}
	host, err := hostname(clusterConfig.ClusterAPI)
	if err != nil {
		return err
	}

	cfg, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		return err
	}
	cfg.Clusters[host] = &api.Cluster{
		Server:                   clusterConfig.ClusterAPI,
		CertificateAuthorityData: ca,
	}

	if err := addContext(cfg, ip, clusterConfig, ca, adminContext, "kubeadmin", clusterConfig.KubeAdminPass); err != nil {
		return err
	}
	if err := addContext(cfg, ip, clusterConfig, ca, developerContext, "developer", "developer"); err != nil {
		return err
	}

	if cfg.CurrentContext == "" {
		cfg.CurrentContext = adminContext
	}

	return clientcmd.WriteToFile(*cfg, kubeconfig)
}

func certificateAuthority(clusterConfig *ClusterConfig) ([]byte, error) {
	bin, err := ioutil.ReadFile(clusterConfig.KubeConfig)
	if err != nil {
		return nil, err
	}
	builtin, err := clientcmd.Load(bin)
	if err != nil {
		return nil, err
	}
	cluster, ok := builtin.Clusters["crc"]
	if !ok {
		return nil, fmt.Errorf("crc cluster not found in kubeconfig %s", clusterConfig.KubeConfig)
	}
	return cluster.CertificateAuthorityData, nil
}

func hostname(clusterAPI string) (string, error) {
	p, err := url.Parse(clusterAPI)
	if err != nil {
		return "", err
	}
	return p.Host, nil
}

func addContext(cfg *api.Config, ip string, clusterConfig *ClusterConfig, ca []byte, context, username, password string) error {
	host, err := hostname(clusterConfig.ClusterAPI)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	ok := roots.AppendCertsFromPEM(ca)
	if !ok {
		return fmt.Errorf("failed to parse root certificate")
	}
	token, err := tokencmd.RequestToken(&restclient.Config{
		Host: clusterConfig.ClusterAPI,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    roots,
				MinVersion: tls.VersionTLS12,
			},
			DialContext: func(ctx gocontext.Context, network, address string) (net.Conn, error) {
				port := strings.SplitN(address, ":", 2)[1]
				dialer := net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}
				return dialer.Dial(network, fmt.Sprintf("%s:%s", ip, port))
			},
		},
	}, nil, username, password)
	if err != nil {
		return err
	}
	cfg.AuthInfos[username] = &api.AuthInfo{
		Token: token,
	}
	cfg.Contexts[context] = &api.Context{
		Cluster:   host,
		AuthInfo:  username,
		Namespace: "default",
	}
	return nil
}

// getGlobalKubeConfigPath returns the path to the first entry in the KUBECONFIG environment variable
// or if KUBECONFIG is not set then $HOME/.kube/config
func getGlobalKubeConfigPath() string {
	pathList := filepath.SplitList(os.Getenv("KUBECONFIG"))
	if len(pathList) > 0 {
		// Tools should write to the last entry in the KUBECONFIG file instead of the first one.
		// oc cluster up also does the same.
		return pathList[len(pathList)-1]
	}
	return filepath.Join(constants.GetHomeDir(), ".kube", "config")
}
