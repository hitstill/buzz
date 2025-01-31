package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/hitstill/buzz/formatter"
	"github.com/jroimartin/gocui"
	"github.com/nsf/termbox-go"
)

const (
	ALL_VIEWS = ""

	URL_VIEW              = "url"
	URL_PARAMS_VIEW       = "get"
	REQUEST_METHOD_VIEW   = "method"
	REQUEST_DATA_VIEW     = "data"
	REQUEST_HEADERS_VIEW  = "headers"
	STATUSLINE_VIEW       = "status-line"
	SEARCH_VIEW           = "search"
	RESPONSE_HEADERS_VIEW = "response-headers"
	RESPONSE_BODY_VIEW    = "response-body"

	SEARCH_PROMPT_VIEW              = "prompt"
	POPUP_VIEW                      = "popup_view"
	AUTOCOMPLETE_VIEW               = "autocomplete_view"
	ERROR_VIEW                      = "error_view"
	HISTORY_VIEW                    = "history"
	SAVE_DIALOG_VIEW                = "save-dialog"
	SAVE_RESPONSE_DIALOG_VIEW       = "save-response-dialog"
	LOAD_REQUEST_DIALOG_VIEW        = "load-request-dialog"
	SAVE_REQUEST_FORMAT_DIALOG_VIEW = "save-request-format-dialog"
	SAVE_REQUEST_DIALOG_VIEW        = "save-request-dialog"
	SAVE_RESULT_VIEW                = "save-result"
	METHOD_LIST_VIEW                = "method-list"
	HELP_VIEW                       = "help"
)

var VIEW_TITLES = map[string]string{
	POPUP_VIEW:                      "Info",
	ERROR_VIEW:                      "Error",
	HISTORY_VIEW:                    "History",
	SAVE_RESPONSE_DIALOG_VIEW:       "Save Response (enter to submit, ctrl+q to cancel)",
	LOAD_REQUEST_DIALOG_VIEW:        "Load Request (enter to submit, ctrl+q to cancel)",
	SAVE_REQUEST_DIALOG_VIEW:        "Save Request (enter to submit, ctrl+q to cancel)",
	SAVE_REQUEST_FORMAT_DIALOG_VIEW: "Choose export format",
	SAVE_RESULT_VIEW:                "Save Result (press enter to close)",
	METHOD_LIST_VIEW:                "Methods",
	HELP_VIEW:                       "Help",
}

type position struct {
	// value = prc * MAX + abs
	pct float32
	abs int
}

type viewPosition struct {
	x0, y0, x1, y1 position
}

var VIEW_POSITIONS = map[string]viewPosition{
	URL_VIEW: {
		position{0.0, 0},
		position{0.0, 0},
		position{1.0, -2},
		position{0.0, 3},
	},
	URL_PARAMS_VIEW: {
		position{0.0, 0},
		position{0.0, 3},
		position{0.3, 0},
		position{0.25, 0},
	},
	REQUEST_METHOD_VIEW: {
		position{0.0, 0},
		position{0.25, 0},
		position{0.3, 0},
		position{0.25, 2},
	},
	REQUEST_DATA_VIEW: {
		position{0.0, 0},
		position{0.25, 2},
		position{0.3, 0},
		position{0.5, 1},
	},
	REQUEST_HEADERS_VIEW: {
		position{0.0, 0},
		position{0.5, 1},
		position{0.3, 0},
		position{1.0, -3},
	},
	RESPONSE_HEADERS_VIEW: {
		position{0.3, 0},
		position{0.0, 3},
		position{1.0, -2},
		position{0.25, 2},
	},
	RESPONSE_BODY_VIEW: {
		position{0.3, 0},
		position{0.25, 2},
		position{1.0, -2},
		position{1.0, -3},
	},
	STATUSLINE_VIEW: {
		position{0.0, -1},
		position{1.0, -4},
		position{1.0, 0},
		position{1.0, -1},
	},
	SEARCH_VIEW: {
		position{0.0, 7},
		position{1.0, -3},
		position{1.0, -1},
		position{1.0, -1},
	},
	ERROR_VIEW: {
		position{0.0, 0},
		position{0.0, 0},
		position{1.0, -2},
		position{1.0, -2},
	},
	SEARCH_PROMPT_VIEW: {
		position{0.0, -1},
		position{1.0, -3},
		position{0.0, 8},
		position{1.0, -1},
	},
	POPUP_VIEW: {
		position{0.5, -9999}, // set before usage using len(msg)
		position{0.5, -1},
		position{0.5, -9999}, // set before usage using len(msg)
		position{0.5, 1},
	},
	AUTOCOMPLETE_VIEW: {
		position{0, -9999},
		position{0, -9999},
		position{0, -9999},
		position{0, -9999},
	},
}

type viewProperties struct {
	title    string
	frame    bool
	editable bool
	wrap     bool
	editor   gocui.Editor
	text     string
}

var VIEW_PROPERTIES = map[string]viewProperties{
	URL_VIEW: {
		title:    "URL - press F1 for help",
		frame:    true,
		editable: true,
		wrap:     false,
		editor:   &singleLineEditor{&defaultEditor},
	},
	URL_PARAMS_VIEW: {
		title:    "URL params",
		frame:    true,
		editable: true,
		wrap:     false,
		editor:   &defaultEditor,
	},
	REQUEST_METHOD_VIEW: {
		title:    "Method",
		frame:    true,
		editable: true,
		wrap:     false,
		editor:   &singleLineEditor{&defaultEditor},
		text:     DEFAULT_METHOD,
	},
	REQUEST_DATA_VIEW: {
		title:    "Request data (POST/PUT/PATCH)",
		frame:    true,
		editable: true,
		wrap:     false,
		editor:   &defaultEditor,
	},
	REQUEST_HEADERS_VIEW: {
		title:    "Request headers",
		frame:    true,
		editable: true,
		wrap:     false,
		editor: &AutocompleteEditor{&defaultEditor, func(str string) []string {
			return completeFromSlice(str, REQUEST_HEADERS)
		}, []string{}, false},
	},
	RESPONSE_HEADERS_VIEW: {
		title:    "Response headers",
		frame:    true,
		editable: true,
		wrap:     true,
		editor:   nil, // should be set using a.getViewEditor(g)
	},
	RESPONSE_BODY_VIEW: {
		title:    "Response body",
		frame:    true,
		editable: true,
		wrap:     true,
		editor:   nil, // should be set using a.getViewEditor(g)
	},
	SEARCH_VIEW: {
		title:    "",
		frame:    false,
		editable: true,
		wrap:     false,
		editor:   &singleLineEditor{&SearchEditor{&defaultEditor}},
	},
	STATUSLINE_VIEW: {
		title:    "",
		frame:    false,
		editable: false,
		wrap:     false,
		editor:   nil,
		text:     "",
	},
	SEARCH_PROMPT_VIEW: {
		title:    "",
		frame:    false,
		editable: false,
		wrap:     false,
		editor:   nil,
		text:     SEARCH_PROMPT,
	},
	POPUP_VIEW: {
		title:    "Info",
		frame:    true,
		editable: false,
		wrap:     false,
		editor:   nil,
	},
	AUTOCOMPLETE_VIEW: {
		title:    "",
		frame:    false,
		editable: false,
		wrap:     false,
		editor:   nil,
	},
}

var VIEWS = []string{
	URL_VIEW,
	URL_PARAMS_VIEW,
	REQUEST_METHOD_VIEW,
	REQUEST_DATA_VIEW,
	REQUEST_HEADERS_VIEW,
	SEARCH_VIEW,
	RESPONSE_HEADERS_VIEW,
	RESPONSE_BODY_VIEW,
}

var defaultEditor ViewEditor

const (
	MIN_WIDTH  = 60
	MIN_HEIGHT = 20
)

type ViewEditor struct {
	app           *App
	g             *gocui.Gui
	backTabEscape bool
	origEditor    gocui.Editor
}

type AutocompleteEditor struct {
	wuzzEditor         *ViewEditor
	completions        func(string) []string
	currentCompletions []string
	isAutocompleting   bool
}

type SearchEditor struct {
	wuzzEditor *ViewEditor
}

// The singleLineEditor removes multi lines capabilities
type singleLineEditor struct {
	wuzzEditor gocui.Editor
}

// Editor funcs

func (e *ViewEditor) Edit(v *gocui.View, key gocui.Key, ch rune, mod gocui.Modifier) {
	// handle back-tab (\033[Z) sequence
	if e.backTabEscape {
		if ch == 'Z' {
			e.app.PrevView(e.g, nil)
			e.backTabEscape = false
			return
		} else {
			e.origEditor.Edit(v, 0, '[', gocui.ModAlt)
		}
	}
	if ch == '[' && mod == gocui.ModAlt {
		e.backTabEscape = true
		return
	}

	// disable infinite down scroll
	if key == gocui.KeyArrowDown && mod == gocui.ModNone {
		_, cY := v.Cursor()
		_, err := v.Line(cY)
		if err != nil {
			return
		}
	}

	e.origEditor.Edit(v, key, ch, mod)
}

var symbolPattern = regexp.MustCompile("[a-zA-Z0-9-]+$")

func getLastSymbol(str string) string {
	return symbolPattern.FindString(str)
}

func completeFromSlice(str string, completions []string) []string {
	completed := []string{}
	if str == "" || strings.TrimRight(str, " \n") != str {
		return completed
	}
	for _, completion := range completions {
		if strings.HasPrefix(completion, str) && str != completion {
			completed = append(completed, completion)
		}
	}
	return completed
}

func (e *AutocompleteEditor) Edit(v *gocui.View, key gocui.Key, ch rune, mod gocui.Modifier) {
	if key != gocui.KeyEnter {
		e.wuzzEditor.Edit(v, key, ch, mod)
	}

	cx, cy := v.Cursor()
	line, err := v.Line(cy)
	trimmedLine := line[:cx]

	if err != nil {
		e.wuzzEditor.Edit(v, key, ch, mod)
		return
	}

	lastSymbol := getLastSymbol(trimmedLine)
	if key == gocui.KeyEnter && e.isAutocompleting {
		currentCompletion := e.currentCompletions[0]
		shouldDelete := true
		if len(e.currentCompletions) == 1 {
			shouldDelete = false
		}

		if shouldDelete {
			for range lastSymbol {
				v.EditDelete(true)
			}
		}
		for _, char := range currentCompletion {
			v.EditWrite(char)
		}
		closeAutocomplete(e.wuzzEditor.g)
		e.isAutocompleting = false
		return
	} else if key == gocui.KeyEnter {
		e.wuzzEditor.Edit(v, key, ch, mod)
	}

	closeAutocomplete(e.wuzzEditor.g)
	e.isAutocompleting = false

	completions := e.completions(lastSymbol)
	e.currentCompletions = completions

	cx, cy = v.Cursor()
	sx, _ := v.Size()
	ox, oy, _, _, _ := e.wuzzEditor.g.ViewPosition(v.Name())

	maxWidth := sx - cx
	maxHeight := 10

	if len(completions) > 0 {
		comps := completions
		x := ox + cx
		y := oy + cy
		if len(comps) == 1 {
			comps[0] = comps[0][len(lastSymbol):]
		} else {
			y += 1
			x -= len(lastSymbol)
			maxWidth += len(lastSymbol)
		}
		showAutocomplete(comps, x, y, maxWidth, maxHeight, e.wuzzEditor.g)
		e.isAutocompleting = true
	}
}

func (e *SearchEditor) Edit(v *gocui.View, key gocui.Key, ch rune, mod gocui.Modifier) {
	e.wuzzEditor.Edit(v, key, ch, mod)
	e.wuzzEditor.g.Update(func(g *gocui.Gui) error {
		e.wuzzEditor.app.PrintBody(g)
		return nil
	})
}

// The singleLineEditor removes multi lines capabilities
func (e singleLineEditor) Edit(v *gocui.View, key gocui.Key, ch rune, mod gocui.Modifier) {
	switch {
	case (ch != 0 || key == gocui.KeySpace) && mod == 0:
		e.wuzzEditor.Edit(v, key, ch, mod)
		// At the end of the line the default gcui editor adds a whitespace
		// Force him to remove
		ox, _ := v.Cursor()
		if ox > 1 && ox >= len(v.Buffer())-2 {
			v.EditDelete(false)
		}
		return
	case key == gocui.KeyEnter:
		return
	case key == gocui.KeyArrowRight:
		ox, _ := v.Cursor()
		if ox >= len(v.Buffer())-1 {
			return
		}
	case key == gocui.KeyHome || key == gocui.KeyArrowUp:
		v.SetCursor(0, 0)
		v.SetOrigin(0, 0)
		return
	case key == gocui.KeyEnd || key == gocui.KeyArrowDown:
		width, _ := v.Size()
		lineWidth := len(v.Buffer()) - 1
		if lineWidth > width {
			v.SetOrigin(lineWidth-width, 0)
			lineWidth = width - 1
		}
		v.SetCursor(lineWidth, 0)
		return
	}
	e.wuzzEditor.Edit(v, key, ch, mod)
}

//

func (a *App) getResponseViewEditor(g *gocui.Gui) gocui.Editor {
	return &ViewEditor{a, g, false, gocui.EditorFunc(func(v *gocui.View, key gocui.Key, ch rune, mod gocui.Modifier) {
	})}
}

func (p position) getCoordinate(max int) int {
	return int(p.pct*float32(max)) + p.abs
}

func setView(g *gocui.Gui, viewName string) (*gocui.View, error) {
	maxX, maxY := g.Size()
	position := VIEW_POSITIONS[viewName]
	return g.SetView(viewName,
		position.x0.getCoordinate(maxX+1),
		position.y0.getCoordinate(maxY+1),
		position.x1.getCoordinate(maxX+1),
		position.y1.getCoordinate(maxY+1))
}

func setViewProperties(v *gocui.View, name string) {
	v.FgColor = gocui.ColorGreen
	v.Title = VIEW_PROPERTIES[name].title
	v.Frame = VIEW_PROPERTIES[name].frame
	v.Editable = VIEW_PROPERTIES[name].editable
	v.Wrap = VIEW_PROPERTIES[name].wrap
	v.Editor = VIEW_PROPERTIES[name].editor
	setViewTextAndCursor(v, VIEW_PROPERTIES[name].text)
}

func (a *App) Layout(g *gocui.Gui) error {
	maxX, maxY := g.Size()

	if maxX < MIN_WIDTH || maxY < MIN_HEIGHT {
		if v, err := setView(g, ERROR_VIEW); err != nil {
			if err != gocui.ErrUnknownView {
				return err
			}
			setViewDefaults(v)
			v.Title = VIEW_TITLES[ERROR_VIEW]
			g.Cursor = false
			fmt.Fprintln(v, "Terminal is too small")
		}
		return nil
	}
	if _, err := g.View(ERROR_VIEW); err == nil {
		g.DeleteView(ERROR_VIEW)
		g.Cursor = true
		a.setView(g)
	}

	for _, name := range []string{RESPONSE_HEADERS_VIEW, RESPONSE_BODY_VIEW} {
		vp := VIEW_PROPERTIES[name]
		vp.editor = a.getResponseViewEditor(g)
		VIEW_PROPERTIES[name] = vp
	}

	if a.config.General.DefaultURLScheme != "" && !strings.HasSuffix(a.config.General.DefaultURLScheme, "://") {
		p := VIEW_PROPERTIES[URL_VIEW]
		p.text = a.config.General.DefaultURLScheme + "://"
		VIEW_PROPERTIES[URL_VIEW] = p
	}

	for _, name := range []string{
		URL_VIEW,
		URL_PARAMS_VIEW,
		REQUEST_METHOD_VIEW,
		REQUEST_DATA_VIEW,
		REQUEST_HEADERS_VIEW,
		RESPONSE_HEADERS_VIEW,
		RESPONSE_BODY_VIEW,
		STATUSLINE_VIEW,
		SEARCH_PROMPT_VIEW,
		SEARCH_VIEW,
	} {
		if v, err := setView(g, name); err != nil {
			if err != gocui.ErrUnknownView {
				return err
			}
			setViewProperties(v, name)
		}
	}
	refreshStatusLine(a, g)

	return nil
}

func (a *App) NextView(g *gocui.Gui, v *gocui.View) error {
	a.viewIndex = (a.viewIndex + 1) % len(VIEWS)
	return a.setView(g)
}

func (a *App) PrevView(g *gocui.Gui, v *gocui.View) error {
	a.viewIndex = (a.viewIndex - 1 + len(VIEWS)) % len(VIEWS)
	return a.setView(g)
}

func (a *App) setView(g *gocui.Gui) error {
	a.closePopup(g, a.currentPopup)
	_, err := g.SetCurrentView(VIEWS[a.viewIndex])
	return err
}

func (a *App) setViewByName(g *gocui.Gui, name string) error {
	for i, v := range VIEWS {
		if v == name {
			a.viewIndex = i
			return a.setView(g)
		}
	}
	return fmt.Errorf("view not found")
}

func popup(g *gocui.Gui, msg string) {
	pos := VIEW_POSITIONS[POPUP_VIEW]
	pos.x0.abs = -len(msg)/2 - 1
	pos.x1.abs = len(msg)/2 + 1
	VIEW_POSITIONS[POPUP_VIEW] = pos

	p := VIEW_PROPERTIES[POPUP_VIEW]
	p.text = msg
	VIEW_PROPERTIES[POPUP_VIEW] = p

	if v, err := setView(g, POPUP_VIEW); err != nil {
		if err != gocui.ErrUnknownView {
			return
		}
		setViewProperties(v, POPUP_VIEW)
		g.SetViewOnTop(POPUP_VIEW)
	}
}

func closeAutocomplete(g *gocui.Gui) {
	g.DeleteView(AUTOCOMPLETE_VIEW)
}

func showAutocomplete(completions []string, left, top, maxWidth, maxHeight int, g *gocui.Gui) {
	// Get the width of the widest completion
	completionsWidth := 0
	for _, completion := range completions {
		thisCompletionWidth := len(completion)
		if thisCompletionWidth > completionsWidth {
			completionsWidth = thisCompletionWidth
		}
	}

	// Get the width and height of the autocomplete window
	width := minInt(completionsWidth, maxWidth)
	height := minInt(len(completions), maxHeight)

	newPos := viewPosition{
		x0: position{0, left},
		y0: position{0, top},
		x1: position{0, left + width + 1},
		y1: position{0, top + height + 1},
	}

	VIEW_POSITIONS[AUTOCOMPLETE_VIEW] = newPos

	p := VIEW_PROPERTIES[AUTOCOMPLETE_VIEW]
	p.text = strings.Join(completions, "\n")
	VIEW_PROPERTIES[AUTOCOMPLETE_VIEW] = p

	if v, err := setView(g, AUTOCOMPLETE_VIEW); err != nil {
		if err != gocui.ErrUnknownView {
			return
		}
		setViewProperties(v, AUTOCOMPLETE_VIEW)
		v.BgColor = gocui.ColorBlue
		v.FgColor = gocui.ColorDefault
		g.SetViewOnTop(AUTOCOMPLETE_VIEW)
	}
}

func writeSortedHeaders(output io.Writer, h http.Header) {
	hkeys := make([]string, 0, len(h))
	for hname := range h {
		hkeys = append(hkeys, hname)
	}

	sort.Strings(hkeys)

	for _, hname := range hkeys {
		fmt.Fprintf(output, "\x1b[0;33m%v:\x1b[0;0m %v\n", hname, strings.Join(h[hname], ","))
	}
}

func (a *App) PrintBody(g *gocui.Gui) {
	g.Update(func(g *gocui.Gui) error {
		if len(a.history) == 0 {
			return nil
		}
		req := a.history[a.historyIndex]
		if req.RawResponseBody == nil {
			return nil
		}
		vrb, _ := g.View(RESPONSE_BODY_VIEW)
		vrb.Clear()

		var responseFormatter formatter.ResponseFormatter
		responseFormatter = req.Formatter

		vrb.Title = VIEW_PROPERTIES[vrb.Name()].title + " " + responseFormatter.Title()

		search_text := getViewValue(g, "search")
		if search_text == "" || !responseFormatter.Searchable() {
			err := responseFormatter.Format(vrb, req.RawResponseBody)
			if err != nil {
				fmt.Fprintf(vrb, "Error: cannot decode response body: %v", err)
				return nil
			}
			if _, err := vrb.Line(0); !a.config.General.PreserveScrollPosition || err != nil {
				vrb.SetOrigin(0, 0)
			}
			return nil
		}
		if !a.config.General.ContextSpecificSearch {
			responseFormatter = DEFAULT_FORMATTER
		}
		vrb.SetOrigin(0, 0)
		results, err := responseFormatter.Search(search_text, req.RawResponseBody)
		if err != nil {
			fmt.Fprint(vrb, "Search error: ", err)
			return nil
		}
		if len(results) == 0 {
			vrb.Title = "No results"
			fmt.Fprint(vrb, "Error: no results")
			return nil
		}
		vrb.Title = fmt.Sprintf("%d results", len(results))
		for _, result := range results {
			fmt.Fprintf(vrb, "-----\n%s\n", result)
		}
		return nil
	})
}

func parseKey(k string) (interface{}, gocui.Modifier, error) {
	mod := gocui.ModNone
	if strings.Index(k, "Alt") == 0 {
		mod = gocui.ModAlt
		k = k[3:]
	}
	switch len(k) {
	case 0:
		return 0, 0, errors.New("empty key string")
	case 1:
		if mod != gocui.ModNone {
			k = strings.ToLower(k)
		}
		return rune(k[0]), mod, nil
	}

	key, found := KEYS[k]
	if !found {
		return 0, 0, fmt.Errorf("unknown key: %v", k)
	}
	return key, mod, nil
}

func (a *App) setKey(g *gocui.Gui, keyStr, commandStr, viewName string) error {
	if commandStr == "" {
		return nil
	}
	key, mod, err := parseKey(keyStr)
	if err != nil {
		return err
	}
	commandParts := strings.SplitN(commandStr, " ", 2)
	command := commandParts[0]
	var commandArgs string
	if len(commandParts) == 2 {
		commandArgs = commandParts[1]
	}
	keyFnGen, found := COMMANDS[command]
	if !found {
		return fmt.Errorf("unknown command: %v", command)
	}
	keyFn := keyFnGen(commandArgs, a)
	if err := g.SetKeybinding(viewName, key, mod, keyFn); err != nil {
		return fmt.Errorf("failed to set key '%v': %v", keyStr, err)
	}
	return nil
}

func (a *App) printViewKeybindings(v io.Writer, viewName string) {
	keys, found := a.config.Keys[viewName]
	if !found {
		return
	}
	mk := make([]string, len(keys))
	i := 0
	for k := range keys {
		mk[i] = k
		i++
	}
	sort.Strings(mk)
	fmt.Fprintf(v, "\n %v\n", viewName)
	for _, key := range mk {
		fmt.Fprintf(v, "  %-15v %v\n", key, keys[key])
	}
}

func (a *App) SetKeys(g *gocui.Gui) error {
	// load config keybindings
	for viewName, keys := range a.config.Keys {
		if viewName == "global" {
			viewName = ALL_VIEWS
		}
		for keyStr, commandStr := range keys {
			if err := a.setKey(g, keyStr, commandStr, viewName); err != nil {
				return err
			}
		}
	}

	g.SetKeybinding(ALL_VIEWS, gocui.KeyF1, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		if a.currentPopup == HELP_VIEW {
			a.closePopup(g, HELP_VIEW)
			return nil
		}

		help, err := a.CreatePopupView(HELP_VIEW, 60, 40, g)
		if err != nil {
			return err
		}
		help.Title = VIEW_TITLES[HELP_VIEW]
		help.Highlight = false
		fmt.Fprint(help, "Keybindings:\n")
		a.printViewKeybindings(help, "global")
		for _, viewName := range VIEWS {
			if _, found := a.config.Keys[viewName]; !found {
				continue
			}
			a.printViewKeybindings(help, viewName)
		}
		g.SetViewOnTop(HELP_VIEW)
		g.SetCurrentView(HELP_VIEW)
		return nil
	})

	g.SetKeybinding(ALL_VIEWS, gocui.MouseRelease, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		if g.CurrentView() != v {
			g.SetCurrentView(v.Name())
			v.SetCursor(0, 0)
		}
		return nil
	})

	g.SetKeybinding(ALL_VIEWS, gocui.KeyF11, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		a.config.General.FollowRedirects = !a.config.General.FollowRedirects
		refreshStatusLine(a, g)
		return nil
	})

	g.SetKeybinding(REQUEST_METHOD_VIEW, gocui.KeyEnter, gocui.ModNone, a.ToggleMethodList)

	cursDown := func(g *gocui.Gui, v *gocui.View) error {
		cx, cy := v.Cursor()
		v.SetCursor(cx, cy+1)
		return nil
	}
	cursUp := func(g *gocui.Gui, v *gocui.View) error {
		cx, cy := v.Cursor()
		if cy > 0 {
			cy -= 1
		}
		v.SetCursor(cx, cy)
		return nil
	}
	// history key bindings
	g.SetKeybinding(HISTORY_VIEW, gocui.KeyArrowDown, gocui.ModNone, cursDown)
	g.SetKeybinding(HISTORY_VIEW, gocui.KeyArrowUp, gocui.ModNone, cursUp)
	g.SetKeybinding(HISTORY_VIEW, gocui.KeyEnter, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		_, cy := v.Cursor()
		// TODO error
		if len(a.history) <= cy {
			return nil
		}
		a.restoreRequest(g, cy)
		return nil
	})

	// method key bindings
	g.SetKeybinding(REQUEST_METHOD_VIEW, gocui.KeyArrowDown, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		value := strings.TrimSpace(v.Buffer())
		for i, val := range METHODS {
			if val == value && i != len(METHODS)-1 {
				setViewTextAndCursor(v, METHODS[i+1])
			}
		}
		return nil
	})

	g.SetKeybinding(REQUEST_METHOD_VIEW, gocui.KeyArrowUp, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		value := strings.TrimSpace(v.Buffer())
		for i, val := range METHODS {
			if val == value && i != 0 {
				setViewTextAndCursor(v, METHODS[i-1])
			}
		}
		return nil
	})
	g.SetKeybinding(METHOD_LIST_VIEW, gocui.KeyArrowDown, gocui.ModNone, cursDown)
	g.SetKeybinding(METHOD_LIST_VIEW, gocui.KeyArrowUp, gocui.ModNone, cursUp)
	g.SetKeybinding(METHOD_LIST_VIEW, gocui.KeyEnter, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		_, cy := v.Cursor()
		v, _ = g.View(REQUEST_METHOD_VIEW)
		setViewTextAndCursor(v, METHODS[cy])
		a.closePopup(g, METHOD_LIST_VIEW)
		return nil
	})
	g.SetKeybinding(SAVE_REQUEST_FORMAT_DIALOG_VIEW, gocui.KeyArrowDown, gocui.ModNone, cursDown)
	g.SetKeybinding(SAVE_REQUEST_FORMAT_DIALOG_VIEW, gocui.KeyArrowUp, gocui.ModNone, cursUp)

	g.SetKeybinding(SAVE_DIALOG_VIEW, gocui.KeyCtrlQ, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		a.closePopup(g, SAVE_DIALOG_VIEW)
		return nil
	})

	g.SetKeybinding(SAVE_RESULT_VIEW, gocui.KeyEnter, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		a.closePopup(g, SAVE_RESULT_VIEW)
		return nil
	})
	return nil
}

func (a *App) closePopup(g *gocui.Gui, viewname string) {
	_, err := g.View(viewname)
	if err == nil {
		a.currentPopup = ""
		g.DeleteView(viewname)
		g.SetCurrentView(VIEWS[a.viewIndex%len(VIEWS)])
		g.Cursor = true
	}
}

// CreatePopupView create a popup like view
func (a *App) CreatePopupView(name string, width, height int, g *gocui.Gui) (v *gocui.View, err error) {
	// Remove any concurrent popup
	a.closePopup(g, a.currentPopup)

	g.Cursor = false
	maxX, maxY := g.Size()
	if height > maxY-4 {
		height = maxY - 4
	}
	if width > maxX-4 {
		width = maxX - 4
	}
	v, err = g.SetView(name, maxX/2-width/2-1, maxY/2-height/2-1, maxX/2+width/2, maxY/2+height/2+1)
	if err != nil && err != gocui.ErrUnknownView {
		return
	}
	err = nil
	v.Wrap = false
	v.Frame = true
	v.Highlight = true
	v.SelFgColor = gocui.ColorYellow
	v.SelBgColor = gocui.ColorDefault
	a.currentPopup = name
	return
}

func (a *App) ToggleHistory(g *gocui.Gui, _ *gocui.View) (err error) {
	// Destroy if present
	if a.currentPopup == HISTORY_VIEW {
		a.closePopup(g, HISTORY_VIEW)
		return
	}

	history, err := a.CreatePopupView(HISTORY_VIEW, 100, len(a.history), g)
	if err != nil {
		return
	}

	history.Title = VIEW_TITLES[HISTORY_VIEW]

	if len(a.history) == 0 {
		setViewTextAndCursor(history, "[!] No items in history")
		return
	}
	for i, r := range a.history {
		req_str := fmt.Sprintf("[%02d] %v %v", i, r.Method, r.Url)
		if r.GetParams != "" {
			req_str += fmt.Sprintf("?%v", strings.Replace(r.GetParams, "\n", "&", -1))
		}
		if r.Data != "" {
			req_str += fmt.Sprintf(" %v", strings.Replace(r.Data, "\n", "&", -1))
		}
		if r.Headers != "" {
			req_str += fmt.Sprintf(" %v", strings.Replace(r.Headers, "\n", ";", -1))
		}
		fmt.Fprintln(history, req_str)
	}
	g.SetViewOnTop(HISTORY_VIEW)
	g.SetCurrentView(HISTORY_VIEW)
	history.SetCursor(0, a.historyIndex)
	return
}

func (a *App) SaveRequest(g *gocui.Gui, _ *gocui.View) (err error) {
	// Destroy if present
	if a.currentPopup == SAVE_REQUEST_FORMAT_DIALOG_VIEW {
		a.closePopup(g, SAVE_REQUEST_FORMAT_DIALOG_VIEW)
		return
	}
	// Create the view listing the possible formats
	popup, err := a.CreatePopupView(SAVE_REQUEST_FORMAT_DIALOG_VIEW, 30, len(EXPORT_FORMATS), g)
	if err != nil {
		return err
	}

	popup.Title = VIEW_TITLES[SAVE_REQUEST_FORMAT_DIALOG_VIEW]

	// Populate the popup witht the available formats
	for _, r := range EXPORT_FORMATS {
		fmt.Fprintln(popup, r.name)
	}

	g.SetViewOnTop(SAVE_REQUEST_FORMAT_DIALOG_VIEW)
	g.SetCurrentView(SAVE_REQUEST_FORMAT_DIALOG_VIEW)
	popup.SetCursor(0, 0)

	// Bind the enter key, when the format is chosen, save the choice and open
	// the save popup
	g.SetKeybinding(SAVE_REQUEST_FORMAT_DIALOG_VIEW, gocui.KeyEnter, gocui.ModNone, func(g *gocui.Gui, v *gocui.View) error {
		// Save the format index
		_, format := v.Cursor()
		// Open the Save popup
		return a.OpenSaveDialog(VIEW_TITLES[SAVE_REQUEST_DIALOG_VIEW], g,
			func(g *gocui.Gui, _ *gocui.View) error {
				defer a.closePopup(g, SAVE_DIALOG_VIEW)
				saveLocation := getViewValue(g, SAVE_DIALOG_VIEW)

				r := Request{
					Url:       getViewValue(g, URL_VIEW),
					Method:    getViewValue(g, REQUEST_METHOD_VIEW),
					GetParams: getViewValue(g, URL_PARAMS_VIEW),
					Data:      getViewValue(g, REQUEST_DATA_VIEW),
					Headers:   getViewValue(g, REQUEST_HEADERS_VIEW),
				}

				// Export the request using the chosent format
				request := EXPORT_FORMATS[format].export(r)

				// Write the file
				ioerr := os.WriteFile(saveLocation, []byte(request), 0o644)

				saveResult := fmt.Sprintf("Request saved successfully in %s", EXPORT_FORMATS[format].name)
				if ioerr != nil {
					saveResult = "Error saving request: " + ioerr.Error()
				}
				viewErr := a.OpenSaveResultView(saveResult, g)

				return viewErr
			},
		)
	})

	return
}

func (a *App) ToggleMethodList(g *gocui.Gui, _ *gocui.View) (err error) {
	// Destroy if present
	if a.currentPopup == METHOD_LIST_VIEW {
		a.closePopup(g, METHOD_LIST_VIEW)
		return
	}

	method, err := a.CreatePopupView(METHOD_LIST_VIEW, 50, len(METHODS), g)
	if err != nil {
		return
	}
	method.Title = VIEW_TITLES[METHOD_LIST_VIEW]

	cur := getViewValue(g, REQUEST_METHOD_VIEW)

	for i, r := range METHODS {
		fmt.Fprintln(method, r)
		if cur == r {
			method.SetCursor(0, i)
		}
	}
	g.SetViewOnTop(METHOD_LIST_VIEW)
	g.SetCurrentView(METHOD_LIST_VIEW)
	return
}

func (a *App) OpenSaveDialog(title string, g *gocui.Gui, save func(g *gocui.Gui, v *gocui.View) error) error {
	dialog, err := a.CreatePopupView(SAVE_DIALOG_VIEW, 60, 1, g)
	if err != nil {
		return err
	}
	g.Cursor = true

	dialog.Title = title
	dialog.Editable = true
	dialog.Wrap = false

	currentDir, err := os.Getwd()
	if err != nil {
		currentDir = ""
	}
	currentDir += "/"

	setViewTextAndCursor(dialog, currentDir)

	g.SetViewOnTop(SAVE_DIALOG_VIEW)
	g.SetCurrentView(SAVE_DIALOG_VIEW)
	dialog.SetCursor(0, len(currentDir))
	g.DeleteKeybinding(SAVE_DIALOG_VIEW, gocui.KeyEnter, gocui.ModNone)
	g.SetKeybinding(SAVE_DIALOG_VIEW, gocui.KeyEnter, gocui.ModNone, save)
	return nil
}

func (a *App) OpenSaveResultView(saveResult string, g *gocui.Gui) (err error) {
	popupTitle := VIEW_TITLES[SAVE_RESULT_VIEW]
	saveResHeight := 1
	saveResWidth := len(saveResult) + 1
	if len(popupTitle)+2 > saveResWidth {
		saveResWidth = len(popupTitle) + 2
	}
	maxX, _ := g.Size()
	if saveResWidth > maxX {
		saveResHeight = saveResWidth/maxX + 1
		saveResWidth = maxX
	}

	saveResultPopup, err := a.CreatePopupView(SAVE_RESULT_VIEW, saveResWidth, saveResHeight, g)
	saveResultPopup.Title = popupTitle
	setViewTextAndCursor(saveResultPopup, saveResult)
	g.SetViewOnTop(SAVE_RESULT_VIEW)
	g.SetCurrentView(SAVE_RESULT_VIEW)
	return err
}

func (a *App) restoreRequest(g *gocui.Gui, idx int) {
	if idx < 0 || idx >= len(a.history) {
		return
	}
	a.closePopup(g, HISTORY_VIEW)
	a.historyIndex = idx
	r := a.history[idx]

	v, _ := g.View(URL_VIEW)
	setViewTextAndCursor(v, r.Url)

	v, _ = g.View(REQUEST_METHOD_VIEW)
	setViewTextAndCursor(v, r.Method)

	v, _ = g.View(URL_PARAMS_VIEW)
	setViewTextAndCursor(v, r.GetParams)

	v, _ = g.View(REQUEST_DATA_VIEW)
	setViewTextAndCursor(v, r.Data)

	v, _ = g.View(REQUEST_HEADERS_VIEW)
	setViewTextAndCursor(v, r.Headers)

	v, _ = g.View(RESPONSE_HEADERS_VIEW)
	setViewTextAndCursor(v, r.ResponseHeaders)

	a.PrintBody(g)
}

func refreshStatusLine(a *App, g *gocui.Gui) {
	sv, _ := g.View(STATUSLINE_VIEW)
	a.statusLine.Update(sv, a)
}

func initApp(a *App, g *gocui.Gui) {
	g.Cursor = true
	g.Mouse = true
	g.InputEsc = false
	g.BgColor = gocui.ColorDefault
	g.FgColor = gocui.Attribute(termbox.ColorLightBlue)
	g.SetManagerFunc(a.Layout)
}

func getViewValue(g *gocui.Gui, name string) string {
	v, err := g.View(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v.Buffer())
}

func setViewDefaults(v *gocui.View) {
	v.Frame = true
	v.Wrap = false
}

func setViewTextAndCursor(v *gocui.View, s string) {
	v.Clear()
	fmt.Fprint(v, s)
	v.SetCursor(len(s), 0)
}

func minInt(x, y int) int {
	if x < y {
		return x
	}
	return y
}
