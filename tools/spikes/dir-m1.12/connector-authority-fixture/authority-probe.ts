import type {
  PaseoApi,
  PaseoClient,
  PaseoClientConfig,
} from "@getpaseo/client";
import type {
  PluginContext,
  PluginHandlerContext,
} from "@getpaseo/plugin";

type Assert<Value extends true> = Value;
type Equal<Left, Right> =
  (<Value>() => Value extends Left ? 1 : 2) extends
  (<Value>() => Value extends Right ? 1 : 2)
    ? true
    : false;
type Has<Key extends PropertyKey, Value> = Key extends keyof Value ? true : false;

type TopLevelContributionHasNoPaseoApi = Assert<
  Equal<Has<"paseo", PluginContext>, false>
>;
type RpcHandlerHasInjectedPaseoApi = Assert<
  Equal<PluginHandlerContext["paseo"], PaseoApi>
>;
type StandaloneClientOwnsConnectionLifecycle = Assert<
  Equal<Has<"connect", PaseoClient> | Has<"close", PaseoClient>, true>
>;
type PublicClientAcceptsDaemonPassword = Assert<
  Equal<Has<"password", PaseoClientConfig>, true>
>;
type PublicClientAcceptsProxyHeader = Assert<
  Equal<Has<"authHeader", PaseoClientConfig>, true>
>;
type PublicClientHasNoCredentialScope = Assert<
  Equal<
    Has<"scope", PaseoClientConfig> |
      Has<"scopes", PaseoClientConfig> |
      Has<"permissions", PaseoClientConfig>,
    false
  >
>;
type CredentialReachesFullPaseoApi = Assert<
  Equal<
    Has<"agents", PaseoApi> |
      Has<"workspaces", PaseoApi> |
      Has<"config", PaseoApi>,
    true
  >
>;

export type Exact072ConnectorAuthorityProbe = {
  topLevelContributionHasNoPaseoApi: TopLevelContributionHasNoPaseoApi;
  rpcHandlerHasInjectedPaseoApi: RpcHandlerHasInjectedPaseoApi;
  standaloneClientOwnsConnectionLifecycle: StandaloneClientOwnsConnectionLifecycle;
  publicClientAcceptsDaemonPassword: PublicClientAcceptsDaemonPassword;
  publicClientAcceptsProxyHeader: PublicClientAcceptsProxyHeader;
  publicClientHasNoCredentialScope: PublicClientHasNoCredentialScope;
  credentialReachesFullPaseoApi: CredentialReachesFullPaseoApi;
};
