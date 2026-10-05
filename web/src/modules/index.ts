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
      { path: "/compute/services", label: "Services", component: planned(["Compute"], "Services", "Phase 2", "§4, §5.2", "Long-running services with desired count, rolling deploys and autoscaling.") },
      { path: "/compute/tasks", label: "Tasks", component: planned(["Compute"], "Tasks", "Phase 2", "§5.2–5.3", "Every running container across all nodes.") },
      { path: "/compute/jobs", label: "Jobs", component: planned(["Compute"], "Jobs", "Phase 3", "§5.11", "One-off tasks, cron jobs and deploy hooks.") },
      { path: "/compute/deployments", label: "Deployments", component: planned(["Compute"], "Deployments", "Phase 3", "§5.4", "Rollout history, circuit breaker and rollback.") },
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
      { path: "/network/topology", label: "Topology", component: planned(["Network"], "Topology", "Phase 6", "§8.4", "Mesh links with latency, loss and throughput.") },
      { path: "/network/firewall", label: "Firewall", component: planned(["Network"], "Firewall", "Phase 1/6", "§8.3", "Host policies, security groups, hit counters and reachability.") },
      { path: "/network/ipam", label: "IPAM & DNS", component: planned(["Network"], "IPAM & DNS", "Phase 1", "§8.1–8.2, §8.6", "Subnets, service VIPs, task IPs and internal DNS.") },
    ],
  },
  {
    id: "logs",
    label: "Logs",
    icon: ScrollText,
    order: 30,
    pages: [{ path: "/logs", label: "Logs", component: planned([], "Logs", "Phase 2", "§9.2", "Merged per-service logs from every node, live tail and search.") }],
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
      { path: "/health", label: "Services", component: planned(["Health"], "Service health", "Phase 3", "§5.6", "Health, uptime % and response times for every service.") },
      { path: "/health/incidents", label: "Incidents", component: planned(["Health"], "Incidents", "Phase 3", "§5.6", "Incident timeline with causes.") },
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
      { path: "/settings/domains", label: "Domains", component: planned(["Settings"], "Domains", "Phase 0b", "§5.0.2", "sslip.io base domain, your own domain and certificates.") },
      { path: "/settings/updates", label: "Updates", component: planned(["Settings"], "Updates", "Phase 9", "§5.0.1", "Controller upgrades with automatic rollback.") },
    ],
  },
].sort((a, b) => a.order - b.order);
