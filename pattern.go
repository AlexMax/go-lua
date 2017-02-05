package lua

import (
	"strings"
	"unicode"
)

const luaMaxCaptures = 32

const (
	capUnfinished = -1
	capPosition   = -2
)

type matchState struct {
	matchDepth int
	src        *string
	p          *string
	l          *State
	level      int
	capture    [luaMaxCaptures]struct {
		init int
		len  int
	}
}

const maxCCalls = 200
const lEsc = '%'
const specials = "^$*+?.([%-"

func captureToClose(ms *matchState) int {
	level := ms.level
	level--
	for ; level >= 0; level-- {
		if ms.capture[level].len == capUnfinished {
			return level
		}
	}
	Errorf(ms.l, "invalid pattern capture")
	return 0
}

func classend(ms *matchState, ppos int) int {
	switch (*ms.p)[ppos] {
	case lEsc:
		ppos++
		if ppos == len(*ms.p) {
			Errorf(ms.l, "malformed pattern (ends with '%%')")
		}
		return ppos + 1
	case '[':
		ppos++
		if (*ms.p)[ppos] == '^' {
			ppos++
		}
		for { // look for a ']'
			if ppos == len(*ms.p) {
				Errorf(ms.l, "malformed pattern (missing ']')")
			}
			ppos++
			if (*ms.p)[ppos] == lEsc && ppos < len(*ms.p) {
				ppos++ // skip escapes (e.g. `%]')
			}
			if (*ms.p)[ppos] == ']' {
				break
			}
		}
		return ppos + 1
	default:
		return ppos + 1
	}
}

func matchClass(c byte, cl byte) bool {
	var res bool
	var rc, rcl rune = rune(c), rune(cl)
	switch unicode.ToLower(rcl) {
	case 'a':
		res = unicode.IsLetter(rc)
	case 'c':
		res = unicode.IsControl(rc)
	case 'd':
		res = unicode.IsDigit(rc)
	case 'g':
		res = unicode.IsGraphic(rc) && !unicode.IsSpace(rc)
	case 'l':
		res = unicode.IsLower(rc)
	case 'p':
		res = unicode.IsPunct(rc)
	case 's':
		res = unicode.IsSpace(rc)
	case 'u':
		res = unicode.IsUpper(rc)
	case 'w':
		res = unicode.In(rc, unicode.Letter, unicode.Number)
	case 'x':
		res = unicode.In(rc, unicode.Hex_Digit)
	case 'z':
		res = (c == 0)
	default:
		return cl == c
	}
	if unicode.IsLower(rcl) {
		return res
	} else {
		return !res
	}
}

func matchbracketclass(c byte, p string, ppos int, ecpos int) bool {
	sig := true

	if p[ppos+1] == '^' {
		sig = false
		ppos++ // skip the `^'
	}

	for {
		ppos++
		if ppos >= ecpos {
			break
		}

		if p[ppos] == lEsc {
			ppos++
			if matchClass(c, p[ppos]) {
				return sig
			}
		} else if p[ppos+1] == '-' && ppos+2 < ecpos {
			ppos = ppos + 2
			if p[ppos-2] <= c && c <= p[ppos] {
				return sig
			}
		} else if p[ppos] == c {
			return sig
		}
	}

	return !sig
}

func singlematch(ms *matchState, spos int, ppos int, eppos int) bool {
	if spos >= len(*ms.src) {
		return false
	} else {
		var c byte = (*ms.src)[spos]
		switch (*ms.p)[ppos] {
		case '.':
			return true // matches any char
		case lEsc:
			return matchClass(c, (*ms.p)[ppos+1])
		case '[':
			return matchbracketclass(c, *ms.p, ppos, eppos-1)
		default:
			return (*ms.p)[ppos] == c
		}
	}
}

func maxExpand(ms *matchState, spos int, ppos int, eppos int) (int, bool) {
	i := 0 // counts maximum expand for item
	for {
		if singlematch(ms, spos+i, ppos, eppos) {
			i++
		} else {
			break
		}
	}
	// keeps trying to match with the maximum repetitions
	for {
		if i < 0 {
			break
		}
		res, ok := match(ms, spos+i, eppos+1)
		if ok {
			return res, ok
		}
		i--
	}
	return 0, false
}

func minExpand(ms *matchState, spos int, ppos int, eppos int) (int, bool) {
	for {
		res, ok := match(ms, spos, eppos+1)
		if ok {
			return res, true
		} else if singlematch(ms, spos, ppos, eppos) {
			spos++
		} else {
			return 0, false
		}
	}
}

func startCapture(ms *matchState, spos int, ppos int, what int) (int, bool) {
	level := ms.level
	if level >= luaMaxCaptures {
		Errorf(ms.l, "too many captures")
	}
	ms.capture[level].init = spos
	ms.capture[level].len = what
	ms.level = level + 1
	res, ok := match(ms, spos, ppos)
	if !ok { // match failed?
		ms.level-- // undo capture
	}
	return res, ok
}

func endCapture(ms *matchState, spos int, ppos int) (int, bool) {
	l := captureToClose(ms)
	ms.capture[l].len = spos - ms.capture[l].init // close capture
	res, ok := match(ms, spos, ppos)
	if !ok { // match failed?
		ms.capture[l].len = capUnfinished // undo capture
	}
	return res, ok
}

func match(ms *matchState, spos int, ppos int) (int, bool) {
	if ms.matchDepth == 0 {
		Errorf(ms.l, "pattern too complex")
	}
	ms.matchDepth--
	ok := true

	// The default case - return true to goto init
	defaultCase := func() bool {
		eppos := classend(ms, ppos) // points to optional suffix
		// does not match at least once?
		if !singlematch(ms, spos, ppos, eppos) {
			var ep byte = 0
			if eppos != len(*ms.p) {
				ep = (*ms.p)[eppos]
			}
			if ep == '*' || ep == '?' || ep == '-' { // accept empty?
				ppos = eppos + 1
				return true // return match(ms, spos, eppos + 1);
			} else { // '+' or no suffix
				ok = false // fail
			}
		} else { // matched once
			var ep byte = 0
			if eppos != len(*ms.p) {
				ep = (*ms.p)[eppos]
			}
			switch ep {
			case '?': // optional
				res, resOk := match(ms, spos+1, eppos+1)
				if resOk {
					spos = res
				} else {
					ppos = eppos + 1
					return true
				}
			case '+': // 1 or more repetitions
				spos++ // 1 match already done
				fallthrough
			case '*': // 0 or more repetitions
				spos, ok = maxExpand(ms, spos, ppos, eppos)
			case '-': // 0 or more repetitions (minimum)
				spos, ok = minExpand(ms, spos, ppos, eppos)
			default: // no suffix
				spos++
				ppos = eppos
				return true
			}
		}
		return false
	}

init: // using goto's to optimize tail recursion
	if ppos != len(*ms.p) { // end of pattern?
		switch (*ms.p)[ppos] {
		case '(': // start capture
			if (*ms.p)[ppos+1] == ')' {
				spos, ok = startCapture(ms, spos, ppos+2, capPosition)
			} else {
				spos, ok = startCapture(ms, spos, ppos+1, capUnfinished)
			}
		case ')': // end capture
			spos, ok = endCapture(ms, spos, ppos+1)
		case '$':
			if ppos+1 != len(*ms.p) { // is the `$' the last char in pattern?
				if defaultCase() {
					goto init
				}
			} else {
				if spos != len(*ms.src) {
					spos, ok = 0, false
				}
			}
		case lEsc:
			pnext := (*ms.p)[ppos+1]
			switch {
			case pnext == 'b': // balanced string?
				// TODO
			case pnext == 'f': // frontier?
				// TODO
			case pnext >= '0' && pnext <= '9': /* capture results (%0-%9)? */
				// TODO
			default:
				if defaultCase() {
					goto init
				}
			}
		default: // pattern class plus optional suffix
			if defaultCase() {
				goto init
			}
		}
	}
	ms.matchDepth++
	return spos, ok
}

func pushOnecapture(ms *matchState, i int, spos int, epos int) {
	if i >= ms.level {
		if i == 0 { // ms->level == 0, too
			ms.l.PushString((*ms.src)[spos:epos]) // add whole match
		} else {
			Errorf(ms.l, "invalid capture index")
		}
	} else {
		l := ms.capture[i].len
		if l == capUnfinished {
			Errorf(ms.l, "unfinished capture")
		}
		ipos := ms.capture[i].init
		if l == capPosition {
			ms.l.PushInteger(ipos)
		} else {
			ms.l.PushString((*ms.src)[ipos : ipos+l])
		}
	}
}

func pushCaptures(ms *matchState, spos int, epos int, snil bool) int {
	nlevels := 1
	if !(ms.level == 0 && !snil) {
		nlevels = ms.level
	}
	CheckStackWithMessage(ms.l, nlevels, "too many captures")
	for i := 0; i < nlevels; i++ {
		pushOnecapture(ms, i, spos, epos)
	}
	return nlevels
}

func nospecials(p string) bool {
	if strings.IndexAny(p, specials) != -1 {
		return false
	}
	return true
}

func strFindAux(l *State, find bool) int {
	s := CheckString(l, 1)
	p := CheckString(l, 2)

	init := relativePosition(OptInteger(l, 3, 1), len(s))
	if init < 1 {
		init = 1
	} else if init > len(s)+1 { // start after string's end?
		l.PushNil() // cannot find anything
		return 1
	}
	// explicit request or no special characters?
	// FIXME: ToBoolean returns true for invalid index
	if find && (l.Top() >= 4 && l.ToBoolean(4)) || nospecials(p) {
		// do a plain search
		s2 := strings.Index(s[init-1:], p)
		if s2 != -1 {
			l.PushInteger(s2 + init)
			l.PushInteger(s2 + init + len(p) - 1)
			return 2
		}
	} else {
		s1 := init - 1
		anchor := p[0] == '^'
		if anchor {
			p = p[1:] // skip anchor character
		}

		ms := matchState{
			l:          l,
			matchDepth: maxCCalls,
			src:        &s,
			p:          &p,
		}

		for {
			ms.level = 0
			res, ok := match(&ms, s1, 0)
			if ok {
				if find {
					l.PushInteger(s1 + 1)
					l.PushInteger(res)
					return pushCaptures(&ms, 0, 0, true) + 2
				} else {
					return pushCaptures(&ms, s1, res, false)
				}
			}

			if !(s1 < len(*ms.src) && !anchor) {
				break
			}
			s1++
		}
	}

	l.PushNil()
	return 1
}

func strFind(l *State) int {
	return strFindAux(l, true)
}

func strMatch(l *State) int {
	return strFindAux(l, false)
}

func gmatchAux(l *State) int {
	s, _ := l.ToString(UpValueIndex(1))
	p, _ := l.ToString(UpValueIndex(2))

	ms := matchState{
		l:          l,
		matchDepth: maxCCalls,
		src:        &s,
		p:          &p,
	}

	srcpos, _ := l.ToInteger(UpValueIndex(3))
	for ; srcpos < len(*ms.src); srcpos++ {
		ms.level = 0
		epos, ok := match(&ms, srcpos, 0)
		if ok {
			newstart := epos
			if epos == srcpos {
				newstart++
			}
			l.PushInteger(newstart)
			l.Replace(UpValueIndex(3))
			return pushCaptures(&ms, srcpos, epos, false)
		}
	}
	return 0
}

func gmatch(l *State) int {
	CheckString(l, 1)
	CheckString(l, 2)
	l.SetTop(2)
	l.PushInteger(0)
	l.PushGoClosure(gmatchAux, 3)
	return 1
}

func strGsub(l *State) int {
	return 0
}