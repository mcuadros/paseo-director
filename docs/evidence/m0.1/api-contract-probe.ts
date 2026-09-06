import type {
  PaseoAgentHandle,
  PaseoAgentTimelineHandle,
  PaseoApi,
} from "@getpaseo/client";
import type { PluginContext } from "@getpaseo/plugin";

type Assert<T extends true> = T;
type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2)
    ? true
    : false;
type Has<Key extends PropertyKey, Value> = Key extends keyof Value ? true : false;

type RequiredSdkRoots = Assert<
  Equal<"projects" | "workspaces" | "agents" | "providers" | "config", keyof PaseoApi>
>;
type HasAgentArchive = Assert<Has<"archive", PaseoAgentHandle>>;
type HasAgentSend = Assert<Has<"send", PaseoAgentHandle>>;
type HasAgentWait = Assert<Has<"waitForFinish", PaseoAgentHandle>>;
type NoAgentCancel = Assert<Equal<Has<"cancel", PaseoAgentHandle>, false>>;
type NoPermissionResponse = Assert<
  Equal<Has<"respondToPermission", PaseoAgentHandle>, false>
>;
type NoTimelineAppend = Assert<
  Equal<Has<"append", PaseoAgentTimelineHandle>, false>
>;

type RequiredPluginContributions = Assert<
  Equal<
    | "handle"
    | "addSurface"
    | "addSidebarItem"
    | "addWorkspacePanel"
    | "addCommandCenterItem"
    | "addClientSide"
    | "addAttachmentSource"
    | "addTheme"
    | "addTimelineTransformer"
    | "addTimelineRenderer",
    keyof PluginContext
  >
>;
type NoSlashCommandContribution = Assert<
  Equal<Has<"addSlashCommand", PluginContext>, false>
>;

export type PublishedStableContractProbe = {
  requiredSdkRoots: RequiredSdkRoots;
  hasAgentArchive: HasAgentArchive;
  hasAgentSend: HasAgentSend;
  hasAgentWait: HasAgentWait;
  noAgentCancel: NoAgentCancel;
  noPermissionResponse: NoPermissionResponse;
  noTimelineAppend: NoTimelineAppend;
  requiredPluginContributions: RequiredPluginContributions;
  noSlashCommandContribution: NoSlashCommandContribution;
};
