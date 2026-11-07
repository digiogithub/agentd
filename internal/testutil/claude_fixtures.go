package testutil

// Byte fixtures reproducing the end of real Claude Code 2.1.319 turns (from
// the PTY log of a live agentd session, 120x40; here on a 60x6 screen with
// the input line on row 3, 1-based). See docs/ghost-text.md, "Post-turn
// suggestions". Claude Code sets the window title to "<spinner glyph> <topic>"
// while the cursor sits on the input line; ✳ is U+2733 = E2 9C B3, whose 0x9C
// byte the x/ansi parser used to take for an 8-bit ST.
const (
	// claudeBusyInput: input line while working (grey ❯, line cleared),
	// cursor parked after "❯ ".
	claudeBusyInput = "\x1b[1;1H\x1b[38;5;244m────────────\x1b[39m" +
		"\x1b[3;1H\x1b[38;5;246m❯  \x1b[39m\x1b[K" +
		"\x1b[4;1H\x1b[38;5;244m────────────\x1b[39m" +
		"\x1b[3;3H"
	// claudeIdleInput: the final frame redraws "❯ " in the default colour
	// and leaves the cursor at x=2 of the input line.
	claudeIdleInput = "\x1b[3;1H❯  \x1b[6;1H\x1b[3;3H"

	// ClaudePostTurnSuggestion: turn ends with the title "✳ Ping de prueba
	// de agentd", then Claude draws the suggested prompt faint at the
	// cursor (no erase after it) and puts the cursor back on its first cell.
	ClaudePostTurnSuggestion = claudeBusyInput +
		"\x1b]0;✳ Ping de prueba de agentd\x07" +
		claudeIdleInput +
		"\x1b[?25l\x1b[H\r\x1b[2C\x1b[2B\x1b[2m¿Te llegó el pong?\x1b[22m\x1b[6;1H\x1b[3;3H\x1b[?25h"
	// ClaudePostTurnSuggestionText is the suggestion in ClaudePostTurnSuggestion.
	ClaudePostTurnSuggestionText = "¿Te llegó el pong?"

	// ClaudePostTurnEmpty: turn ends with the title "✳ Prueba de agentd" and
	// no suggestion: the input line is empty.
	ClaudePostTurnEmpty = claudeBusyInput +
		"\x1b]0;✳ Prueba de agentd\x07" +
		claudeIdleInput
)
