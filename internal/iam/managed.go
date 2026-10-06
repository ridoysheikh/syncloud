package iam

import "strings"

// Managed policies (§7) are built in and cannot be edited. Project-scoped
// ones are templates: attaching ProjectOwner for "shop" uses the policy
// "ProjectOwner:shop".
type Managed struct {
	Name        string
	Description string
	// PerProject templates take a project; the others are global.
	PerProject bool
	doc        func(project string) Document
}

func allow(actions []string, resources ...string) Statement {
	return Statement{Effect: "Allow", Action: actions, Resource: resources}
}

// ProjectResources are the resources of one project.
func ProjectResources(project string) []string {
	return []string{"srn:syncloud:project/" + project, "srn:syncloud:project/" + project + "/*"}
}

// Read actions: every get, list, query and check operation.
var readActions = []string{"*:Get*", "*:List*", "*:Query*", "*:Tail*", "*:Check*", "*:Lookup*", "*:Preview*", "*:Validate*"}

// Actions a project's developers use on its resources.
var developerActions = []string{
	"service:*", "task:*", "job:*", "build:*", "autoscaling:*", "logs:*", "metrics:*", "traffic:*", "health:*",
	"project:SetSharedVariables", "traefik:*Middleware", "registry:Pull", "registry:Push",
}

// Actions of deployers: read, deploy, scale, roll back, run jobs and builds.
var deployerActions = []string{
	"service:ApplyService", "service:ScaleService", "service:RollbackService", "task:RestartTask",
	"job:RunJob", "job:RunService", "job:CancelRun", "build:StartBuild", "build:DeployBuild", "registry:Pull", "registry:Push",
}

var managed = []Managed{
	{Name: "AdministratorAccess", Description: "Everything, everywhere", doc: func(string) Document {
		return Document{Version: Version, Statement: []Statement{allow([]string{"*"}, "*")}}
	}},
	{Name: "ReadOnly", Description: "Read everything; change nothing", doc: func(string) Document {
		return Document{Version: Version, Statement: []Statement{allow(readActions, "*")}}
	}},
	{Name: "BillingViewer", Description: "Quotas and usage", doc: func(string) Document {
		return Document{Version: Version, Statement: []Statement{allow([]string{"quota:Get*", "quota:List*", "usage:*"}, "*")}}
	}},
	{Name: "ProjectOwner", Description: "Everything in one project, including its security groups, quotas view and members' access", PerProject: true, doc: func(p string) Document {
		return Document{Version: Version, Statement: []Statement{
			allow([]string{"*"}, ProjectResources(p)...),
			allow(readActions, "srn:syncloud:node/*", "srn:syncloud:registry/"+p+"/*"),
			allow([]string{"registry:*"}, "srn:syncloud:registry/"+p+"/*"),
			// Owners run the project within its quota; they cannot change it.
			{Effect: "Deny", Action: []string{"quota:Set*", "quota:Delete*"}, Resource: []string{"*"}},
		}}
	}},
	{Name: "Developer", Description: "Build, deploy, operate and debug one project's services", PerProject: true, doc: func(p string) Document {
		return Document{Version: Version, Statement: []Statement{
			allow(append(append([]string{}, readActions...), developerActions...), ProjectResources(p)...),
			allow([]string{"registry:Pull", "registry:Push", "registry:Get*", "registry:List*"}, "srn:syncloud:registry/"+p+"/*"),
		}}
	}},
	{Name: "Deployer", Description: "Read one project and deploy, scale and roll back its services (CI)", PerProject: true, doc: func(p string) Document {
		return Document{Version: Version, Statement: []Statement{
			allow(append(append([]string{}, readActions...), deployerActions...), ProjectResources(p)...),
			allow([]string{"registry:Pull", "registry:Push"}, "srn:syncloud:registry/"+p+"/*"),
		}}
	}},
}

// ManagedPolicies lists the built-in policies.
func ManagedPolicies() []Managed { return managed }

// ManagedDocument returns a managed policy's document: "ReadOnly", or
// "Developer:shop" for a project template.
func ManagedDocument(name string) (Document, bool) {
	base, project, scoped := strings.Cut(name, ":")
	for _, m := range managed {
		if m.Name == base && m.PerProject == scoped && (!scoped || project != "") {
			return m.doc(project), true
		}
	}
	return Document{}, false
}
