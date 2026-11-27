import { TargetPage } from "../features/targets/TargetPage";
import { TargetsPage } from "../features/targets/TargetsPage";
import type { RouteObject } from "react-router";
import { LazyComparePage } from "../features/compare/LazyComparePage";
import { PlannerPage } from "../features/planner/PlannerPage";
import { HostPage } from "../features/hosts/HostPage";
import { ManagedTargetPage } from "../features/managed-targets/ManagedTargetPage";
import { ManagedTargetsPage } from "../features/managed-targets/ManagedTargetsPage";
import { NewRunPage } from "../features/new-run/NewRunPage";
import { OverviewPage } from "../features/overview/OverviewPage";
import { ExperimentsListPage } from "../features/experiments/ExperimentsListPage";
import { LazyExperimentPage } from "../features/experiments/LazyExperimentPage";
import { RunOverviewPage } from "../features/run-detail/RunOverviewPage";
import { RunsListPage } from "../features/runs/RunsListPage";
import { SchedulePage } from "../features/schedules/SchedulePage";
import { SchedulesPage } from "../features/schedules/SchedulesPage";
import { NewSessionPage } from "../features/tuning/NewSessionPage";
import { SessionPage } from "../features/tuning/SessionPage";
import { TuningPage } from "../features/tuning/TuningPage";
import { AccountPage } from "../features/auth/AccountPage";
import { UsersPage } from "../features/users/UsersPage";
import { Layout } from "./Layout";
import { NotFoundPage } from "./NotFoundPage";

// / is the overview (managed vLLMs, tuning, recent runs).
export const routes: RouteObject[] = [
  {
    element: <Layout />,
    children: [
      { path: "/", element: <OverviewPage /> },
      { path: "/runs", element: <RunsListPage /> },
      { path: "/runs/new", element: <NewRunPage /> },
      { path: "/runs/:runId", element: <RunOverviewPage /> },
      { path: "/compare", element: <LazyComparePage /> },
      { path: "/experiments", element: <ExperimentsListPage /> },
      { path: "/experiments/:name", element: <LazyExperimentPage /> },
      { path: "/planner", element: <PlannerPage /> },
      { path: "/planner/:scenarioId", element: <PlannerPage /> },
      { path: "/schedules", element: <SchedulesPage /> },
      { path: "/schedules/:scheduleId", element: <SchedulePage /> },
      { path: "/targets", element: <TargetsPage /> },
      { path: "/targets/:targetId", element: <TargetPage /> },
      { path: "/managed-targets", element: <ManagedTargetsPage /> },
      { path: "/managed-targets/:targetId", element: <ManagedTargetPage /> },
      { path: "/hosts/:hostId", element: <HostPage /> },
      { path: "/tuning", element: <TuningPage /> },
      { path: "/tuning/new", element: <NewSessionPage /> },
      { path: "/tuning/:sessionId", element: <SessionPage /> },
      { path: "/account", element: <AccountPage /> },
      { path: "/users", element: <UsersPage /> },
      { path: "*", element: <NotFoundPage /> },
    ],
  },
];
