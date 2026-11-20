// Package apidocs serves pinned, local Swagger UI assets without a CDN.
package apidocs

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets/* openapi.json index.html
var files embed.FS

func Handler() http.Handler {
	sub, _ := fs.Sub(files, ".")
	server := http.FileServer(http.FS(sub))
	return http.StripPrefix("/swagger/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		server.ServeHTTP(w, r)
	}))
}

// DocumentationOnlyHandler disables Try it out on a standalone public docs host.
// The full backend retains Handler with authenticated request execution enabled.
func DocumentationOnlyHandler() http.Handler {
	base := Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/swagger/assets/init.js" {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Write([]byte(`window.onload=function(){SwaggerUIBundle({url:'/swagger/openapi.json',dom_id:'#swagger-ui',deepLinking:true,persistAuthorization:false,validatorUrl:null,supportedSubmitMethods:[]});};`))
			return
		}
		base.ServeHTTP(w, r)
	})
}
