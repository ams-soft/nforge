package email

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

const simpleMJML = `<mjml>
  <mj-body>
    <mj-section>
      <mj-column>
        <mj-text font-size="20px" color="#F45E43">Hello World</mj-text>
        <mj-button href="https://example.com/start">Start</mj-button>
      </mj-column>
    </mj-section>
  </mj-body>
</mjml>`

func TestRenderMJMLNeedsNoModule(t *testing.T) {
	html, err := RenderMJML(context.Background(), simpleMJML)
	if err != nil {
		t.Fatal(err)
	}
	// A structural fixture, not a byte-for-byte snapshot: the output must be a
	// complete HTML email whose key elements and inlined styles are present.
	for _, want := range []string{"<!doctype html>", "<html", "Hello World", `href="https://example.com/start"`, "Start", "#F45E43", "font-size:20px", "<body"} {
		if !strings.Contains(strings.ToLower(html), strings.ToLower(want)) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Contains(html, "<mjml") || strings.Contains(html, "<mj-") {
		t.Error("MJML tags left in the output")
	}
}

func TestRenderMJMLInvalid(t *testing.T) {
	for name, src := range map[string]string{
		"empty":     "",
		"not mjml":  "<html><body>x</body></html>",
		"unclosed":  "<mjml><mj-body><mj-section>",
		"bad child": "<mjml><mj-body><mj-text>x</mj-text></mj-body></mjml>",
		"unknown":   "<mjml><mj-body><mj-nope/></mj-body></mjml>",
	} {
		if _, err := RenderMJML(context.Background(), src); !errors.Is(err, ErrMJML) {
			t.Errorf("%s: err = %v, want ErrMJML", name, err)
		}
	}
}

func TestRenderMJMLDeniesIncludes(t *testing.T) {
	src := `<mjml><mj-body><mj-include path="/etc/passwd" type="css" /><mj-include path="./x.mjml" /></mj-body></mjml>`
	_, err := RenderMJML(context.Background(), src)
	if !errors.Is(err, ErrMJML) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "root:") {
		t.Fatal("file content in the error")
	}
}

func TestRenderMJMLLimits(t *testing.T) {
	big := "<mjml><mj-body><mj-section><mj-column><mj-text>" + strings.Repeat("a", MaxMJMLSourceBytes) + "</mj-text></mj-column></mj-section></mj-body></mjml>"
	if _, err := RenderMJML(context.Background(), big); !errors.Is(err, ErrMJMLTooLarge) {
		t.Fatalf("source limit: %v", err)
	}
	// A source under the limit whose output is over: many sections.
	sec := "<mj-section><mj-column><mj-text>x</mj-text></mj-column></mj-section>"
	n := MaxMJMLSourceBytes / len(sec)
	src := "<mjml><mj-body>" + strings.Repeat(sec, n) + "</mj-body></mjml>"
	if _, err := RenderMJML(context.Background(), src); err != nil && !errors.Is(err, ErrMJMLTooLarge) {
		t.Fatalf("output limit: %v", err)
	}
}

func TestRenderMJMLCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RenderMJML(ctx, simpleMJML); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if _, err := RenderMJML(ctx, simpleMJML); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderMJMLConcurrent(t *testing.T) {
	want, err := RenderMJML(context.Background(), simpleMJML)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := RenderMJML(context.Background(), simpleMJML)
			if err != nil || len(got) != len(want) {
				t.Errorf("err=%v len=%d want %d", err, len(got), len(want))
			}
		}()
	}
	wg.Wait()
}

func TestRenderedHTMLIsSendable(t *testing.T) {
	html, err := RenderMJML(context.Background(), simpleMJML)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := build(testFrom, Message{To: "a@example.org", Subject: "s", HTML: html}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func FuzzRenderMJML(f *testing.F) {
	f.Add(simpleMJML)
	f.Add("<mjml><mj-body><mj-include path='../../x'/></mj-body></mjml>")
	f.Add("<mjml>")
	f.Fuzz(func(t *testing.T, src string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		html, err := RenderMJML(ctx, src)
		if err == nil && len(html) > MaxMJMLOutputBytes {
			t.Fatal("output over the limit")
		}
	})
}
