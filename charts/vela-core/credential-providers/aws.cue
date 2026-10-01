// Setup template for the cluster-gateway AWS credential plugin.
// `vela cluster setup --provider aws` prints setup; `vela cluster join --provider aws`
// stores providerConfig as the cluster's provider-config.
import (
	"encoding/json"
	"strings"
)

context: cluster: string

parameter: {
	// Region of the EKS cluster.
	region: =~#"^[a-z]{2}(-[a-z]+)+-[0-9]+$"#
	// AWS account that owns the EKS cluster.
	accountId: =~#"^[0-9]{12}$"#
	// Role of the AWS plugin itself: its IRSA or EKS Pod Identity role.
	hubRoleArn: =~#"^arn:aws[a-z-]*:iam::[0-9]{12}:role/[\w+=,.@/-]+$"#
	// EKS cluster name, if different from the KubeVela cluster name.
	eksCluster: (*context.cluster | string) & =~#"^[0-9A-Za-z][A-Za-z0-9_-]{0,99}$"#
	// Role cluster-gateway assumes for this cluster. The plugin's allowlist must allow it.
	roleName: (*"vela-\(context.cluster)" | string) & =~#"^[\w+=,.@-]{1,64}$"#
	// The AWS plugin's --session-name.
	sessionPrefix: *"cluster-gateway" | string
	// Kubernetes group the role maps to in the cluster.
	kubernetesGroup: *"vela:cluster-gateway" | string
	// EKS access policy to associate, e.g. AmazonEKSEditPolicy. Omit to grant
	// the group its permissions with RBAC.
	accessPolicy?: =~#"^[A-Za-z0-9]+$"#
}

// The plugin assumes the role with this session name; the trust policy pins it.
_session: "\(parameter.sessionPrefix)-\(context.cluster)" & strings.MaxRunes(64)
_partition: strings.Split(parameter.hubRoleArn, ":")[1]
_roleArn: "arn:\(_partition):iam::\(parameter.accountId):role/\(parameter.roleName)"

_trust: json.Marshal({
	Version: "2012-10-17"
	Statement: [{
		Effect: "Allow"
		Principal: AWS: parameter.hubRoleArn
		Action: "sts:AssumeRole"
		Condition: StringEquals: "sts:RoleSessionName": _session
	}]
})

providerConfig: {
	region:      parameter.region
	clusterName: parameter.eksCluster
	roleArn:     _roleArn
}

setup: [{
	title: "In account \(parameter.accountId), create the role cluster-gateway assumes for cluster \(context.cluster). Only \(parameter.hubRoleArn) may assume it, with session name \(_session)."
	commands: ["aws iam create-role --role-name \(parameter.roleName) --assume-role-policy-document '\(_trust)'"]
}, {
	title: "Map the role into EKS cluster \(parameter.eksCluster)."
	commands: [
		"aws eks create-access-entry --region \(parameter.region) --cluster-name \(parameter.eksCluster) --principal-arn \(_roleArn) --kubernetes-groups \(parameter.kubernetesGroup)",
		if parameter.accessPolicy != _|_ {
			"aws eks associate-access-policy --region \(parameter.region) --cluster-name \(parameter.eksCluster) --principal-arn \(_roleArn) --policy-arn arn:aws:eks::aws:cluster-access-policy/\(parameter.accessPolicy) --access-scope type=cluster"
		},
	]
}, if parameter.accessPolicy == _|_ {
	title: "Grant group \(parameter.kubernetesGroup) its permissions in the cluster with RBAC."
	commands: []
}, {
	title: "Allow the role in the AWS plugin: its allowedRoleArns must match \(_roleArn), e.g. arn:\(_partition):iam::\(parameter.accountId):role/vela-{cluster}."
	commands: []
}]
