package extmeta

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
)

// DescribeError returns a user-facing message that contains only safe, categorized details.
func DescribeError(err error) string {
	var httpErr *HTTPStatusError
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrSSRFBlocked):
		return "目标地址被网络安全策略拦截。若路由器使用非标准 Fake-IP 地址段或将域名解析为局域网地址，请在网络代理设置中配置显式代理。"
	case errors.Is(err, ErrInvalidProxyURL):
		return "网络代理设置中的代理 URL 无效，请检查协议、主机和端口。"
	case errors.Is(err, ErrInvalidManifest):
		return "元数据仓库清单 JSON 无效，请使用 repo.json 的原始文件链接（Raw）。"
	case errors.Is(err, ErrInvalidConfig), errors.Is(err, ErrInvalidURL):
		return "元数据配置无效，请检查数据源设置。"
	case errors.Is(err, ErrCircuitOpen):
		return "上游封面请求触发熔断，请稍后重试。"
	case errors.As(err, &httpErr):
		return fmt.Sprintf("上游返回 HTTP %d，请检查元数据链接或稍后重试。", httpErr.StatusCode)
	case errors.Is(err, ErrUnavailable):
		return "元数据源暂不可用。"
	case errors.Is(err, ErrTooManyRedirects):
		return "上游重定向次数过多，请检查元数据链接。"
	case errors.As(err, &syntaxErr), errors.As(err, &typeErr):
		return "上游元数据 JSON 格式无效。"
	case errors.Is(err, os.ErrNotExist):
		return "找不到元数据文件，请检查数据源设置。"
	case errors.Is(err, os.ErrPermission):
		return "无法访问元数据文件，请检查文件权限。"
	}
	switch diagnostics.ClassifyError(err) {
	case "CANCELED":
		return "元数据请求已取消。"
	case "TIMEOUT":
		return "元数据请求超时，请检查网络或代理设置后重试。"
	case "DNS_ERROR":
		return "无法解析上游域名，请检查 DNS 或代理设置。"
	case "TLS_ERROR":
		return "与上游建立 TLS 连接失败，请检查证书或代理设置。"
	case "CONN_REFUSED":
		return "连接被拒绝，请检查上游服务和代理是否可用。"
	case "CONN_RESET":
		return "连接中断，请检查网络或代理设置后重试。"
	case "CHECKSUM_MISMATCH":
		return "元数据下载的校验和不匹配，请重新下载。"
	case "DB_ERROR":
		return "无法读取或索引元数据数据库，请检查数据源格式。"
	default:
		return "元数据操作失败，请检查数据源和网络设置。"
	}
}

func metadataDiagnostic(stage, action, level string, err error, proxyURL string) diagnostics.Event {
	event := diagnostics.Event{
		Component: "extmeta",
		Stage:     stage,
		Action:    action,
		Level:     level,
		Code:      diagnostics.ClassifyError(err),
		Meta:      make(map[string]any),
	}
	if err != nil {
		// Error strings can contain arbitrary upstream data. Use a fixed description
		// as the input to the log sanitizers.
		event.Detail = diagnostics.SanitizeText(bangumi.SanitizeProxyError(errors.New(DescribeError(err)), proxyURL))
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		event.Meta["httpStatus"] = httpErr.StatusCode
	}
	return event
}
