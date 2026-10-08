import {
  Activity,
  Boxes,
  Cpu,
  Database,
  FolderKanban,
  GitBranch,
  HeartPulse,
  KeyRound,
  LayoutDashboard,
  Network,
  Plug,
  ScrollText,
  Settings,
  SquareTerminal,
  HardDrive,
} from "lucide-react";
import type { DashboardModule } from "./types";
import { OverviewPage } from "./overview/OverviewPage";
import { DatabasesPage } from "./databases/DatabasesPage";
import { DatabasePage } from "./databases/DatabasePage";
import { NewDatabaseWizard } from "./databases/NewDatabaseWizard";
import { PgRolePage } from "./databases/pg/Roles";
import { PgRestorePage } from "./databases/pg/Restore";
import { CredentialsPage } from "./iam/CredentialsPage";
import {
  AuditPage,
  DevicePage,
  GroupPage,
  GroupsPage,
  NewUserPage,
  PoliciesPage,
  PolicyPage,
  RolePage,
  RolesPage,
  SecurityPage,
  UserPage,
  UsersPage,
} from "./iam/IamPages";
import { QuotaPage, QuotasPage } from "./quota/QuotasPage";
import { ApiPage } from "./api/ApiPage";
import { NodesPage } from "./compute/NodesPage";
import { NodePage } from "./compute/NodePage";
import { EdgeNodesPage, NodePoolPage, NodePoolsPage } from "./compute/NodePoolsPage";
import { NewProjectPage, ProjectsPage } from "./projects/ProjectsPage";
import { ProjectPage } from "./projects/ProjectPage";
import { NewServiceWizard } from "./projects/NewServiceWizard";
import { ServicePage } from "./compute/ServicePage";
import { DeploymentPage } from "./compute/Deployments";
import { TasksPage } from "./compute/TasksPage";
import { JobsPage } from "./compute/JobsPage";
import { IncidentsPage, ServiceHealthPage } from "./health/HealthPages";
import { RegistryDashboard, RepositoriesPage } from "./registry/RegistryPages";
import { UpstreamsPage } from "./registry/UpstreamsPage";
import { PlatformPage } from "./settings/PlatformPage";
import { DomainsPage } from "./settings/DomainsPage";
import { BackupsPage } from "./settings/BackupsPage";
import { UpdatesPage } from "./settings/UpdatesPage";
import { BucketPage, EndpointFormPage, EndpointPage, StoragePage } from "./storage/StoragePages";
import { TopologyPage } from "./network/TopologyPage";
import { FirewallPage, FirewallPolicyPage } from "./network/FirewallPage";
import { SecurityGroupPage, SecurityGroupsPage } from "./network/SecurityGroups";
import { IpamPage } from "./network/IpamPage";
import { MiddlewarePage, RoutingPage } from "./network/RoutingPage";
import { TraefikPage } from "./network/TraefikPage";
import { LogsPage } from "./logs/LogsPage";
import { TrafficPage } from "./traffic/Traffic";
import { AlertsPage } from "./monitoring/AlertsPage";
import { MetricsExplorerPage } from "./monitoring/MetricsExplorer";
import { AlertRulePage } from "./monitoring/AlertRulePage";
import { BuildsPage, GitSourcesPage } from "./git/GitPages";
import { GitConnectionPage, GitConnectPage, IntegrationsPage } from "./integrations/IntegrationsPages";

/** The module registry. The side nav and routes are built from this list. */
export const modules: DashboardModule[] = [
  {
    id: "overview",
    label: "Overview",
    icon: LayoutDashboard,
    order: 0,
    pages: [{ path: "/", label: "Overview", component: OverviewPage }],
  },
  {
    // Services only exist inside a project environment, so they are reached
    // through their project (no cluster-wide service list).
    id: "projects",
    label: "Projects",
    icon: FolderKanban,
    order: 5,
    pages: [
      { path: "/projects", label: "Projects", component: ProjectsPage },
      { path: "/projects/quotas", label: "Quotas & usage", component: QuotasPage },
      { path: "/projects/quotas/$project", label: "Quota", component: QuotaPage, hidden: true },
      { path: "/projects/quotas/$project/$env", label: "Quota", component: QuotaPage, hidden: true },
      { path: "/projects/new", label: "New project", component: NewProjectPage, hidden: true },
      { path: "/projects/$project", label: "Project", component: ProjectPage, hidden: true },
      { path: "/projects/$project/$env", label: "Project", component: ProjectPage, hidden: true },
      { path: "/projects/$project/$env/new-service", label: "New service", component: NewServiceWizard, hidden: true },
      { path: "/projects/$project/$env/services/$name", label: "Service", component: ServicePage, hidden: true },
      { path: "/projects/$project/$env/services/$name/deployments/$id", label: "Deployment", component: DeploymentPage, hidden: true },
    ],
  },
  {
    id: "compute",
    label: "Compute",
    icon: Cpu,
    order: 10,
    pages: [
      { path: "/compute/tasks", label: "Tasks", component: TasksPage },
      { path: "/compute/jobs", label: "Jobs", component: JobsPage },
      { path: "/compute/nodes", label: "Nodes", component: NodesPage },
      { path: "/compute/nodes/$name", label: "Node", component: NodePage, hidden: true },
      { path: "/compute/node-pools", label: "Node Pools", component: NodePoolsPage },
      { path: "/compute/node-pools/new", label: "New node pool", component: NodePoolPage, hidden: true },
      { path: "/compute/node-pools/$name", label: "Node pool", component: NodePoolPage, hidden: true },
    ],
  },
  {
    id: "network",
    label: "Network",
    icon: Network,
    order: 20,
    pages: [
      { path: "/network/traffic", label: "Traffic", component: TrafficPage },
      { path: "/network/traefik", label: "Traefik", component: TraefikPage },
      { path: "/network/routing", label: "Routing", component: RoutingPage },
      { path: "/network/routing/middlewares/new", label: "New middleware", component: MiddlewarePage, hidden: true },
      { path: "/network/routing/middlewares/$project/$name", label: "Middleware", component: MiddlewarePage, hidden: true },
      { path: "/network/edge", label: "Edge Nodes", component: EdgeNodesPage },
      { path: "/network/topology", label: "Topology", component: TopologyPage },
      { path: "/network/security-groups", label: "Security groups", component: SecurityGroupsPage },
      { path: "/network/security-groups/new", label: "New security group", component: SecurityGroupPage, hidden: true },
      { path: "/network/security-groups/$project/$group", label: "Security group", component: SecurityGroupPage, hidden: true },
      { path: "/network/firewall", label: "Firewall", component: FirewallPage },
      { path: "/network/firewall/policies/new", label: "New host policy", component: FirewallPolicyPage, hidden: true },
      { path: "/network/firewall/policies/$id", label: "Host policy", component: FirewallPolicyPage, hidden: true },
      { path: "/network/ipam", label: "IPAM & DNS", component: IpamPage },
    ],
  },
  {
    id: "logs",
    label: "Logs",
    icon: ScrollText,
    order: 30,
    pages: [{ path: "/logs", label: "Logs", component: LogsPage }],
  },
  {
    id: "storage",
    label: "Storage",
    icon: HardDrive,
    order: 40,
    pages: [
      { path: "/storage", label: "S3", component: StoragePage },
      { path: "/storage/endpoints/new", label: "Add S3 endpoint", component: EndpointFormPage, hidden: true },
      { path: "/storage/endpoints/$endpoint/edit", label: "Edit S3 endpoint", component: EndpointFormPage, hidden: true },
      { path: "/storage/$endpoint", label: "S3 endpoint", component: EndpointPage, hidden: true },
      { path: "/storage/$endpoint/$bucket", label: "Bucket", component: BucketPage, hidden: true },
    ],
  },
  {
    id: "databases",
    label: "Databases",
    icon: Database,
    order: 12,
    permission: "database:ListAllDatabases",
    pages: [
      { path: "/databases", label: "Databases", component: DatabasesPage },
      { path: "/databases/new", label: "New database", component: NewDatabaseWizard, hidden: true },
      { path: "/projects/$project/$env/new-database", label: "New database", component: NewDatabaseWizard, hidden: true },
      { path: "/databases/$name", label: "Database", component: DatabasePage, hidden: true },
      { path: "/databases/$name/roles/new", label: "New role", component: PgRolePage, hidden: true },
      { path: "/databases/$name/restore", label: "Restore", component: PgRestorePage, hidden: true },
      { path: "/databases/$name/roles/$role", label: "Role", component: PgRolePage, hidden: true },
    ],
  },
  {
    id: "registry",
    label: "Registry",
    icon: Boxes,
    order: 50,
    permission: "registry:ListRepositories",
    pages: [
      { path: "/registry", label: "Dashboard", component: RegistryDashboard },
      { path: "/registry/repos", label: "Repositories", component: RepositoriesPage },
      { path: "/registry/upstreams", label: "Upstreams", component: UpstreamsPage },
    ],
  },
  {
    id: "git",
    label: "Git & Builds",
    icon: GitBranch,
    order: 60,
    pages: [
      { path: "/git/sources", label: "Sources", component: GitSourcesPage },
      { path: "/git/builds", label: "Builds", component: BuildsPage },
    ],
  },
  {
    id: "integrations",
    label: "Integrations",
    icon: Plug,
    order: 62,
    pages: [
      { path: "/integrations", label: "All integrations", component: IntegrationsPage },
      { path: "/integrations/git/new", label: "Connect a Git provider", component: GitConnectPage, hidden: true },
      { path: "/integrations/git/$name", label: "Git connection", component: GitConnectionPage, hidden: true },
    ],
  },
  {
    id: "health",
    label: "Health",
    icon: HeartPulse,
    order: 70,
    pages: [
      { path: "/health", label: "Services", component: ServiceHealthPage },
      { path: "/health/incidents", label: "Incidents", component: IncidentsPage },
    ],
  },
  {
    id: "monitoring",
    label: "Monitoring",
    icon: Activity,
    order: 80,
    pages: [
      { path: "/monitoring/metrics", label: "Metrics", component: MetricsExplorerPage },
      { path: "/monitoring/alerts", label: "Alerts", component: AlertsPage },
      { path: "/monitoring/alerts/rules/new", label: "New alert rule", component: AlertRulePage, hidden: true },
      { path: "/monitoring/alerts/rules/$id", label: "Alert rule", component: AlertRulePage, hidden: true },
    ],
  },
  {
    id: "api",
    label: "API & CLI",
    icon: SquareTerminal,
    order: 90,
    pages: [
      { path: "/developers", label: "API & CLI", component: ApiPage },
      { path: "/device", label: "Sign in synctl", component: DevicePage, hidden: true },
    ],
  },
  {
    id: "iam",
    label: "IAM",
    icon: KeyRound,
    order: 100,
    pages: [
      { path: "/iam/users", label: "Users", component: UsersPage },
      { path: "/iam/users/new", label: "New user", component: NewUserPage, hidden: true },
      { path: "/iam/users/$id", label: "User", component: UserPage, hidden: true },
      { path: "/iam/groups", label: "Groups", component: GroupsPage },
      { path: "/iam/groups/new", label: "New group", component: GroupPage, hidden: true },
      { path: "/iam/groups/$id", label: "Group", component: GroupPage, hidden: true },
      { path: "/iam/roles", label: "Roles", component: RolesPage },
      { path: "/iam/roles/new", label: "New role", component: RolePage, hidden: true },
      { path: "/iam/roles/$id", label: "Role", component: RolePage, hidden: true },
      { path: "/iam/policies", label: "Policies", component: PoliciesPage },
      { path: "/iam/policies/new", label: "New policy", component: PolicyPage, hidden: true },
      { path: "/iam/policies/$id", label: "Policy", component: PolicyPage, hidden: true },
      { path: "/iam/keys", label: "Access keys", component: CredentialsPage },
      { path: "/iam/security", label: "My security", component: SecurityPage },
      { path: "/iam/audit", label: "Audit log", component: AuditPage },
    ],
  },
  {
    id: "settings",
    label: "Settings",
    icon: Settings,
    order: 110,
    pages: [
      { path: "/settings/platform", label: "Platform", component: PlatformPage },
      { path: "/settings/domains", label: "Domains", component: DomainsPage },
      { path: "/settings/backups", label: "Backups", component: BackupsPage },
      { path: "/settings/updates", label: "Updates", component: UpdatesPage },
    ],
  },
].sort((a, b) => a.order - b.order);
