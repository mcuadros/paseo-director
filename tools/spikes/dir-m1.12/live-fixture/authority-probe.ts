import type { PaseoApi } from "@getpaseo/client";
import type { PluginContext, PluginHandlerContext } from "@getpaseo/plugin";

type Assert<T extends true> = T;
type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2)
    ? true
    : false;
type Has<Key extends PropertyKey, Value> = Key extends keyof Value ? true : false;

type TopLevelContributionHasNoPaseoApi = Assert<
  Equal<Has<"paseo", PluginContext>, false>
>;
type RpcHandlerHasPaseoApi = Assert<
  Equal<PluginHandlerContext["paseo"], PaseoApi>
>;
type InjectedApiHasNoConnectionLifecycle = Assert<
  Equal<Has<"connect", PaseoApi> | Has<"close", PaseoApi>, false>
>;

export type Exact072AuthorityProbe = {
  topLevelContributionHasNoPaseoApi: TopLevelContributionHasNoPaseoApi;
  rpcHandlerHasPaseoApi: RpcHandlerHasPaseoApi;
  injectedApiHasNoConnectionLifecycle: InjectedApiHasNoConnectionLifecycle;
};
