package nhttp

import (
	"regexp"
	"strings"
	"time"

	"github.com/ams-soft/go-ams-router/middleware"
	"github.com/ams-soft/nforge/config"
)

// securityOptions translates http.security into the router's options. The
// config was validated at load, so middleware.Security does not panic.
func securityOptions(sec config.SecurityConfig) middleware.SecurityOptions {
	o := middleware.SecurityOptions{
		ContentSecurityPolicy: middleware.CSPOptions{
			DisableDefaults: sec.CSP.DisableDefaults,
			ReportOnly:      sec.CSP.ReportOnly,
		},
		CrossOriginEmbedderPolicy: middleware.CrossOriginEmbedderPolicyOptions{Policy: sec.CrossOriginEmbedderPolicy},
		CrossOriginOpenerPolicy:   middleware.CrossOriginOpenerPolicyOptions{Policy: sec.CrossOriginOpenerPolicy},
		CrossOriginResourcePolicy: middleware.CrossOriginResourcePolicyOptions{Policy: sec.CrossOriginResourcePolicy},
		ReferrerPolicy:            middleware.ReferrerPolicyOptions{Policy: sec.ReferrerPolicy},
		StrictTransportSecurity: middleware.HSTSOptions{
			MaxAge:                   int(sec.HSTS.MaxAge / time.Second),
			DisableIncludeSubDomains: sec.HSTS.IncludeSubdomains != nil && !*sec.HSTS.IncludeSubdomains,
			Preload:                  sec.HSTS.Preload,
		},
		XFrameOptions: middleware.XFrameOptionsOptions{Action: sec.FrameOptions},
	}
	if len(sec.CSP.Directives) > 0 {
		o.ContentSecurityPolicy.Directives = make(map[string][]any, len(sec.CSP.Directives))
		for name, values := range sec.CSP.Directives {
			var vs []any // nil removes a default directive
			if values != nil {
				vs = make([]any, len(values))
				for i, v := range values {
					vs[i] = v
				}
			}
			o.ContentSecurityPolicy.Directives[name] = vs
		}
	}
	for _, h := range sec.Disable {
		switch strings.ToLower(h) {
		case "content-security-policy":
			o.ContentSecurityPolicy.Disable = true
		case "cross-origin-opener-policy":
			o.CrossOriginOpenerPolicy.Disable = true
		case "cross-origin-resource-policy":
			o.CrossOriginResourcePolicy.Disable = true
		case "origin-agent-cluster":
			o.OriginAgentCluster.Disable = true
		case "referrer-policy":
			o.ReferrerPolicy.Disable = true
		case "strict-transport-security":
			o.StrictTransportSecurity.Disable = true
		case "x-content-type-options":
			o.XContentTypeOptions.Disable = true
		case "x-dns-prefetch-control":
			o.XDNSPrefetchControl.Disable = true
		case "x-download-options":
			o.XDownloadOptions.Disable = true
		case "x-frame-options":
			o.XFrameOptions.Disable = true
		case "x-permitted-cross-domain-policies":
			o.XPermittedCrossDomainPolicies.Disable = true
		case "x-xss-protection":
			o.XXSSProtection.Disable = true
		}
	}
	return o
}

// corsOptions translates http.cors into the router's options (patterns were
// compiled once at load, so MustCompile cannot fail).
func corsOptions(c config.CORSConfig) middleware.CORSOptions {
	o := middleware.CORSOptions{
		Origin:               c.Origins,
		Methods:              c.Methods,
		AllowedHeaders:       c.AllowedHeaders,
		ExposedHeaders:       c.ExposedHeaders,
		Credentials:          c.Credentials,
		MaxAge:               int(c.MaxAge / time.Second),
		OptionsSuccessStatus: c.OptionsSuccessStatus,
	}
	for _, p := range c.OriginPatterns {
		o.OriginPatterns = append(o.OriginPatterns, regexp.MustCompile(p))
	}
	return o
}

// docsCSPHeader returns the CSP header name http.security uses, or "" when
// it sends none; the docs page then replaces that header with its own policy.
func docsCSPHeader(sec config.SecurityConfig) string {
	if !sec.Enabled {
		return ""
	}
	for _, h := range sec.Disable {
		if strings.EqualFold(h, "content-security-policy") {
			return ""
		}
	}
	if sec.CSP.ReportOnly {
		return "Content-Security-Policy-Report-Only"
	}
	return "Content-Security-Policy"
}
