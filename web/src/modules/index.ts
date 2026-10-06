import {
  Activity,
  Boxes,
  Cpu,
  GitBranch,
  HeartPulse,
  KeyRound,
  LayoutDashboard,
  Network,
  ScrollText,
  Settings,
  SquareTerminal,
  HardDrive,
} from "lucide-react";
import type { DashboardModule } from "./types";
import { OverviewPage } from "./overview/OverviewPage";
import { planned } from "./planned";
import { CredentialsPage } from "./iam/CredentialsPage";
import { NodesPage } from "./compute/NodesPage";
import { ServicesPage } from "./compute/ServicesPage";
import { ServicePage } from "./compute/ServicePage";
import { TasksPage } from "./compute/TasksPage";
import { JobsPage } from "./compute/JobsPage";
import { IncidentsPage, ServiceHealthPage } from "./health/HealthPages";
import { PlatformPage } from "./settings/PlatformPage";
import { DomainsPage } from "./settings/DomainsPage";
import { BackupsPage } from "./settings/BackupsPage";
import { TopologyPage } from "./network/TopologyPage";
import { FirewallPage } from "./network/FirewallPage";
import { LogsPage } from "./logs/LogsPage";

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
    id: "compute",
    label: "Compute",
    icon: Cpu,
    order: 10,
    pages: [
      { path: "/compute/services", label: "Services", component: ServicesPage },
      { path: "/compute/services/$project/$env/$name", label: "Service", component: ServicePage, hidden: true },
      { path: "/compute/tasks", label: "Tasks", component: TasksPage },
      { path: "/compute/jobs", label: "Jobs", component: JobsPage },
      { path: "/compute/nodes", label: "Nodes", component: NodesPage },
      { path: "/compute/node-pools", label: "Node Pools", component: planned(["Compute"], "Node Pools", "Phase 8", "§6.5", "Provider-backed pools and cluster autoscaling.") },
    ],
  },
  {
    id: "network",
    label: "Network",
    icon: Network,
    order: 20,
    pages: [
      { path: "/network/traffic", label: "Traffic", component: planned(["Network"], "Traffic", "Phase 2/5", "§5.7", "Domains, routes, middlewares, certificates and the live traffic map.") },
      { path: "/network/edge", label: "Edge Nodes", component: planned(["Network"], "Edge Nodes", "Phase 8", "§8.5", "Traefik replicas that keep public traffic flowing.") },
      { path: "/network/topology", label: "Topology", component: TopologyPage },
      { path: "/network/firewall", label: "Firewall", component: FirewallPage },
      { path: "/network/ipam", label: "IPAM & DNS", component: planned(["Network"], "IPAM & DNS", "Phase 1", "§8.1–8.2, §8.6", "Subnets, service VIPs, task IPs and internal DNS.") },
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
    pages: [{ path: "/storage", label: "S3", component: planned([], "Storage (S3)", "Phase 4–7", "§16", "S3 endpoints, bindings and the bucket browser.") }],
  },
  {
    id: "registry",
    label: "Registry",
    icon: Boxes,
    order: 50,
    permission: "registry:ListRepositories",
    pages: [
      { path: "/registry", label: "Dashboard", component: planned(["Registry"], "Registry", "Phase 4", "§5.10", "Storage, pushes and pulls, top repositories.") },
      { path: "/registry/repos", label: "Repositories", component: planned(["Registry"], "Repositories", "Phase 4", "§5.10", "ECR-style repositories, images, push commands and lifecycle policies.") },
      { path: "/registry/upstreams", label: "Upstreams", component: planned(["Registry"], "Upstream credentials", "Phase 4", "§5.9", "Credentials for pulling from Docker Hub, GHCR and others.") },
    ],
  },
  {
    id: "git",
    label: "Git & Builds",
    icon: GitBranch,
    order: 60,
    pages: [
      { path: "/git/sources", label: "Sources", component: planned(["Git & Builds"], "Git sources", "Phase 4", "§5.8", "GitHub, GitLab, Gitea and generic Git, with webhooks and polling.") },
      { path: "/git/builds", label: "Builds", component: planned(["Git & Builds"], "Builds", "Phase 4", "§5.8", "Build queue, live build logs and history.") },
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
      { path: "/monitoring/metrics", label: "Metrics", component: planned(["Monitoring"], "Metrics explorer", "Phase 1", "§9.1", "PromQL explorer over VictoriaMetrics.") },
      { path: "/monitoring/alerts", label: "Alerts", component: planned(["Monitoring"], "Alerts", "Phase 5", "§9", "Alert rules and notification channels.") },
    ],
  },
  {
    id: "api",
    label: "API & CLI",
    icon: SquareTerminal,
    order: 90,
    pages: [{ path: "/api", label: "API & CLI", component: planned([], "API & CLI", "Phase 7", "§7.1", "API docs, synctl download and Cloud Shell.") }],
  },
  {
    id: "iam",
    label: "IAM",
    icon: KeyRound,
    order: 100,
    pages: [
      { path: "/iam/users", label: "Users", component: planned(["IAM"], "Users", "Phase 7", "§7", "Users, groups, roles and service accounts.") },
      { path: "/iam/policies", label: "Policies", component: planned(["IAM"], "Policies", "Phase 7", "§7", "JSON policies and the policy simulator.") },
      { path: "/iam/keys", label: "Access keys", component: CredentialsPage },
      { path: "/iam/audit", label: "Audit log", component: planned(["IAM"], "Audit log", "Phase 7", "§7", "Every action with who, what, where and when.") },
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
      { path: "/settings/updates", label: "Updates", component: planned(["Settings"], "Updates", "Phase 9", "§5.0.1", "Controller upgrades with automatic rollback.") },
    ],
  },
].sort((a, b) => a.order - b.order);
