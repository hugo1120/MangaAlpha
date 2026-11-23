package buildinfo

// Version is overridden by release builds with -ldflags -X.
var Version = "v2.9.0"

// Major-version codename metadata (displayed on 3.x footer & v3.0.0 release).
const (
	Codename      = "长夜"
	CodenameEN    = "Long Night"
	CodenameQuote = "愿人类的夜晚，会是个平静祥和的时代。—— 初濑野阿尔法，《横滨购物纪行》"
)
