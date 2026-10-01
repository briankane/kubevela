/*
Copyright 2026 The KubeVela Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/oam-dev/kubevela/pkg/multicluster"
)

const (
	// FlagCredentialProvider names the cluster-gateway credential plugin for a cluster
	FlagCredentialProvider = "credential-provider"
	// FlagProviderConfig is the JSON configuration passed to the credential plugin
	FlagProviderConfig = "provider-config"
)

// credentialProviderOption returns the join option for the flags, or nil when no
// provider is named.
func credentialProviderOption(provider, config string) (*multicluster.JoinClusterCredentialProviderOption, error) {
	if provider == "" {
		return nil, nil
	}
	op := &multicluster.JoinClusterCredentialProviderOption{Name: provider}
	if config != "" {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(config), &obj); err != nil {
			return nil, errors.Errorf("--%s must be a JSON object: %v", FlagProviderConfig, err)
		}
		op.Config = []byte(config)
	}
	return op, nil
}

// storedExecTokenWarning explains that join ran the kubeconfig's exec plugin once
// and stored its token, which stops working when the token expires.
func storedExecTokenWarning(cfg *multicluster.KubeClusterConfig) string {
	if cfg.CredentialProvider != nil || cfg.AuthInfo == nil || cfg.AuthInfo.Exec == nil {
		return ""
	}
	return fmt.Sprintf("Warning: the kubeconfig's exec plugin (%s) was run once and its token stored. "+
		"Short-lived tokens, such as EKS's 15 minutes, stop working when they expire; "+
		"register the cluster with --%s so cluster-gateway mints them itself.\n", cfg.AuthInfo.Exec.Command, FlagCredentialProvider)
}

type eksSetupOptions struct {
	Cluster         string
	Region          string
	EKSCluster      string
	AccountID       string
	HubRoleARN      string
	RoleName        string
	SessionPrefix   string
	KubernetesGroup string
	AccessPolicy    string
}

var (
	awsAccountID = regexp.MustCompile(`^[0-9]{12}$`)
	awsRoleARN   = regexp.MustCompile(`^arn:aws[a-z-]*:iam::[0-9]{12}:role/[\w+=,.@/-]+$`)
	awsRoleName  = regexp.MustCompile(`^[\w+=,.@-]{1,64}$`)
	awsRegion    = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
)

// maxRoleSessionName is the longest RoleSessionName STS accepts.
const maxRoleSessionName = 64

// writeEKSSetup prints the AWS commands that let cluster-gateway reach one EKS
// cluster through its own role, and the join command that registers it.
func writeEKSSetup(w io.Writer, o eksSetupOptions) error {
	if o.Cluster == "" {
		return errors.New("cluster name is required")
	}
	if o.EKSCluster == "" {
		o.EKSCluster = o.Cluster
	}
	if !awsRegion.MatchString(o.Region) {
		return errors.Errorf("invalid or missing --region %q", o.Region)
	}
	if !awsAccountID.MatchString(o.AccountID) {
		return errors.Errorf("--account-id must be a 12-digit AWS account ID, got %q", o.AccountID)
	}
	if !awsRoleARN.MatchString(o.HubRoleARN) {
		return errors.Errorf("--hub-role-arn must be an IAM role ARN, got %q", o.HubRoleARN)
	}
	roleName := strings.ReplaceAll(o.RoleName, "{cluster}", o.Cluster)
	if !awsRoleName.MatchString(roleName) {
		return errors.Errorf("invalid role name %q from --role-name %q", roleName, o.RoleName)
	}
	// The trust policy pins the session name the AWS plugin uses, which it only
	// shortens past this length.
	sessionName := o.SessionPrefix + "-" + o.Cluster
	if len(sessionName) > maxRoleSessionName {
		return errors.Errorf("session name %q exceeds %d characters; use a shorter cluster name or --session-prefix", sessionName, maxRoleSessionName)
	}
	roleARN := fmt.Sprintf("arn:%s:iam::%s:role/%s", strings.SplitN(o.HubRoleARN, ":", 3)[1], o.AccountID, roleName)

	trust, err := json.Marshal(map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{{
			"Effect":    "Allow",
			"Principal": map[string]string{"AWS": o.HubRoleARN},
			"Action":    "sts:AssumeRole",
			"Condition": map[string]map[string]string{"StringEquals": {"sts:RoleSessionName": sessionName}},
		}},
	})
	if err != nil {
		return err
	}
	providerConfig, err := json.Marshal(struct {
		Region      string `json:"region"`
		ClusterName string `json:"clusterName"`
		RoleARN     string `json:"roleArn"`
	}{o.Region, o.EKSCluster, roleARN})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "# 1. In account %s: the role cluster-gateway assumes for cluster %s.\n", o.AccountID, o.Cluster)
	_, _ = fmt.Fprintf(w, "#    Only %s may assume it, and only with session name %s.\n", o.HubRoleARN, sessionName)
	_, _ = fmt.Fprintf(w, "aws iam create-role --role-name %s --assume-role-policy-document '%s'\n\n", roleName, trust)
	_, _ = fmt.Fprintf(w, "# 2. Map the role into EKS cluster %s.\n", o.EKSCluster)
	_, _ = fmt.Fprintf(w, "aws eks create-access-entry --region %s --cluster-name %s --principal-arn %s --kubernetes-groups %s\n", o.Region, o.EKSCluster, roleARN, o.KubernetesGroup)
	if o.AccessPolicy != "" {
		_, _ = fmt.Fprintf(w, "aws eks associate-access-policy --region %s --cluster-name %s --principal-arn %s --policy-arn arn:aws:eks::aws:cluster-access-policy/%s --access-scope type=cluster\n", o.Region, o.EKSCluster, roleARN, o.AccessPolicy)
	} else {
		_, _ = fmt.Fprintf(w, "#    Grant group %s its permissions with RBAC in the cluster, or pass --access-policy.\n", o.KubernetesGroup)
	}
	_, _ = fmt.Fprintf(w, "\n# 3. The cluster-gateway AWS plugin must allow the role, e.g. allowedRoleArns: [\"arn:aws:iam::%s:role/%s\"].\n", o.AccountID, o.RoleName)
	_, _ = fmt.Fprintf(w, "# 4. Register the cluster.\n")
	_, _ = fmt.Fprintf(w, "vela cluster join <kubeconfig> --name %s --%s aws --%s '%s'\n", o.Cluster, FlagCredentialProvider, FlagProviderConfig, providerConfig)
	return nil
}

// NewClusterEKSSetupCommand prints the AWS setup for joining an EKS cluster with
// the cluster-gateway AWS credential plugin.
func NewClusterEKSSetupCommand() *cobra.Command {
	o := eksSetupOptions{}
	cmd := &cobra.Command{
		Use:   "eks-setup CLUSTER",
		Short: "Print the AWS setup for joining an EKS cluster with its own IAM role.",
		Long: "Print the AWS CLI commands that create the IAM role cluster-gateway assumes for one EKS cluster, " +
			"map it into the cluster with an access entry, and the vela cluster join command that registers it. " +
			"Nothing is run and no AWS credentials are needed.",
		Example: "> vela cluster eks-setup prod-eu --region eu-west-1 --account-id 222222222222 \\\n" +
			"    --hub-role-arn arn:aws:iam::111111111111:role/vela-cluster-gateway",
		Args: cobra.ExactArgs(1),
		// Printing needs no connection to the hub.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			o.Cluster = args[0]
			return writeEKSSetup(cmd.OutOrStdout(), o)
		},
	}
	cmd.Flags().StringVar(&o.Region, "region", "", "AWS region of the EKS cluster")
	cmd.Flags().StringVar(&o.EKSCluster, "eks-cluster", "", "EKS cluster name, if different from the KubeVela cluster name")
	cmd.Flags().StringVar(&o.AccountID, "account-id", "", "AWS account ID that owns the EKS cluster")
	cmd.Flags().StringVar(&o.HubRoleARN, "hub-role-arn", "", "IAM role of the cluster-gateway AWS plugin (its IRSA or Pod Identity role)")
	cmd.Flags().StringVar(&o.RoleName, "role-name", "vela-{cluster}", "name of the role to create; {cluster} is replaced by the cluster name")
	cmd.Flags().StringVar(&o.SessionPrefix, "session-prefix", "cluster-gateway", "the AWS plugin's --session-name")
	cmd.Flags().StringVar(&o.KubernetesGroup, "kubernetes-group", "vela:cluster-gateway", "Kubernetes group the role maps to")
	cmd.Flags().StringVar(&o.AccessPolicy, "access-policy", "", "EKS access policy to associate, e.g. AmazonEKSEditPolicy; omit to grant permissions with RBAC")
	return cmd
}
