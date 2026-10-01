// Command wasm is the browser entrypoint of svg2gslide. Compiled with
// GOOS=js GOARCH=wasm, it exposes a global svg2gslideConvert(svgText,
// presentationUrlOrId, accessToken, phase) function returning a Promise
// that resolves to {slideId, slideUrl, phase, requestCount, warnings}, and
// svg2gslideSplit(text) returning the SVGs to convert as [{index, title,
// svg}] (or an Error): the document itself for an SVG, each inline <svg> for
// an HTML page.
//
//go:build js && wasm

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall/js"

	"google.golang.org/api/googleapi"

	"github.com/owulveryck/svg2gslide/internal/convert"
	"github.com/owulveryck/svg2gslide/internal/gslide"
	"github.com/owulveryck/svg2gslide/internal/htmlsvg"
)

func convertFunc(this js.Value, args []js.Value) any {
	if len(args) < 3 {
		return js.Global().Get("Promise").Call("reject",
			js.Global().Get("Error").New("svg2gslideConvert(svgText, presentationUrlOrId, accessToken[, phase])"))
	}
	svgText := args[0].String()
	presRef := args[1].String()
	token := args[2].String()
	phase := ""
	if len(args) > 3 && args[3].Type() == js.TypeString {
		phase = args[3].String()
	}

	var executor js.Func
	executor = js.FuncOf(func(this js.Value, pArgs []js.Value) any {
		resolve, reject := pArgs[0], pArgs[1]
		// Network I/O must run in a goroutine: blocking inside a js.FuncOf
		// callback deadlocks the wasm runtime.
		go func() {
			defer executor.Release()
			fail := func(err error) {
				errObj := js.Global().Get("Error").New(err.Error())
				var apiErr *googleapi.Error
				if errors.As(err, &apiErr) {
					errObj.Set("status", apiErr.Code)
				}
				reject.Invoke(errObj)
			}

			id, err := convert.ExtractPresentationID(presRef)
			if err != nil {
				fail(err)
				return
			}
			ctx := context.Background()
			client, err := gslide.NewClientWithToken(ctx, token)
			if err != nil {
				fail(err)
				return
			}
			pageW, pageH, err := client.PageSize(ctx, id)
			if err != nil {
				fail(err)
				return
			}
			res, err := convert.Convert(convert.Input{
				SVG:     strings.NewReader(svgText),
				Label:   "upload",
				PageW:   pageW,
				PageH:   pageH,
				Phase:   phase,
				Verbose: true,
			})
			if err != nil {
				fail(err)
				return
			}
			// No Drive scope in the browser: embedded images can't be hosted.
			res.DropEmbeddedImages()
			if err := client.BatchUpdate(ctx, id, res.Requests); err != nil {
				fail(err)
				return
			}

			warnings := make([]any, len(res.Warnings))
			for i, w := range res.Warnings {
				warnings[i] = w
			}
			resolve.Invoke(js.ValueOf(map[string]any{
				"slideId":      res.SlideID,
				"slideUrl":     fmt.Sprintf("https://docs.google.com/presentation/d/%s/edit#slide=id.%s", id, res.SlideID),
				"phase":        res.Phase,
				"requestCount": len(res.Requests),
				"warnings":     warnings,
			}))
		}()
		return nil
	})
	return js.Global().Get("Promise").New(executor)
}

func splitFunc(this js.Value, args []js.Value) any {
	// No panics here: a panic in a js.FuncOf callback kills the Go runtime.
	if len(args) < 1 {
		return js.Global().Get("Error").New("svg2gslideSplit(text)")
	}
	text := args[0].String()
	if !htmlsvg.IsHTML([]byte(text)) {
		return js.ValueOf([]any{map[string]any{"index": 0, "title": "", "svg": text}})
	}
	svgs, err := htmlsvg.Extract(strings.NewReader(text))
	if err != nil {
		return js.Global().Get("Error").New(err.Error())
	}
	out := make([]any, len(svgs))
	for i, s := range svgs {
		out[i] = map[string]any{"index": s.Index, "title": s.Title, "svg": string(s.Data)}
	}
	return js.ValueOf(out)
}

func main() {
	js.Global().Set("svg2gslideConvert", js.FuncOf(convertFunc))
	js.Global().Set("svg2gslideSplit", js.FuncOf(splitFunc))
	js.Global().Set("svg2gslideReady", js.ValueOf(true))
	if cb := js.Global().Get("onSvg2gslideReady"); cb.Type() == js.TypeFunction {
		cb.Invoke()
	}
	// Keep the Go runtime alive; if main returns, every later call throws
	// "Go program has already exited".
	select {}
}
