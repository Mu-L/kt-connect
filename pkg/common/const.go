package common

const (
	EnvVarLocalDomain   = "LOCAL_DOMAIN"
	EnvVarConnectMethod = "CONNECT_METHOD"
	ControlBy           = "control-by"
	KubernetesTool      = "kt"
	ComponentConnect    = "connect"
	ComponentExchange   = "exchange"
	ComponentMesh       = "mesh"
	ComponentProvide    = "provide"
	ConnectMethodVpn    = "vpn"
	ConnectMethodSocks  = "socks"
	ConnectMethodSocks5 = "socks5"
	KTVersion           = "kt-version"        // Label used for fetch shadow mark in UI
	KTComponent         = "kt-component"      // Label used for distinguish shadow type
	KTRemoteAddress     = "kt-remote-address" // Label used for fetch pod IP in UI
	KTName              = "kt-name"           // Label used for wait shadow pod ready
	KTConfig            = "kt-config"         // Annotation used for clean up context
	KTLastHeartBeat     = "kt-last-heart-beat"
	YyyyMmDdHhMmSs      = "2006-01-02 15:04:05"
	SshPort             = 22
	Socks4Port          = 1080
	ShadowSocksPort     = 8379
)
