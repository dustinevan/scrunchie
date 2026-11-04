# The README demo. Record it with `make gif` (needs Charm's VHS).
# It runs the offline --demo workspace, so no database is involved, and it
# points HOME at a throwaway directory so saved connections never appear.

Output docs/assets/demo.gif
Output .scratch/tweet/demo.mp4

Set FontSize 16
Set Width 1240
Set Height 720
Set Padding 18
Set Margin 22
Set MarginFill "#b967ff"
Set WindowBar Colorful
Set BorderRadius 12
Set TypingSpeed 70ms
Set Theme { "name": "Plum Night", "background": "#1a1026", "foreground": "#f4eafc", "cursor": "#ff3ea5", "selection": "#5b2386", "black": "#1a1026", "red": "#ff5f5f", "green": "#05ffa1", "yellow": "#ffe45e", "blue": "#01cdfe", "magenta": "#ff71ce", "cyan": "#01cdfe", "white": "#f4eafc", "brightBlack": "#4b3470", "brightRed": "#ff5f5f", "brightGreen": "#05ffa1", "brightYellow": "#ffe45e", "brightBlue": "#01cdfe", "brightMagenta": "#ff71ce", "brightCyan": "#01cdfe", "brightWhite": "#ffffff" }

Hide
Type "export HOME=$PWD/.scratch/vhs-home COLORTERM=truecolor PATH=$PWD/bin:$PATH; clear"
Enter
Show

# A bare start never connects to anything. The mascot waits in results.
Type "scrunch"
Sleep 300ms
Enter
Sleep 2.8s
Ctrl+Q
Sleep 400ms

# The offline demo: run, browse, inspect.
Type "scrunch --demo"
Sleep 300ms
Enter
Sleep 2s
Ctrl+J
Sleep 1.4s
Down@120ms 6
PageUp
Sleep 600ms
Right
Sleep 300ms
Right
Sleep 700ms

# JSON keeps every digit and opens key by key.
Enter
Sleep 1.2s
Down@180ms 4
Right
Sleep 600ms
Down@180ms 2
Right
Sleep 600ms
Down@180ms 3
Right
Sleep 900ms
Down@180ms 3
Sleep 1.6s
Escape
Sleep 600ms

# bytea: same bytes, many views. Row 1 is also a valid UUID.
Right
Sleep 600ms
Type "v"
Sleep 350ms
Type "v"
Sleep 350ms
Type "v"
Sleep 1s
Type "v"
Sleep 1s
Type "v"
Sleep 1.6s
Type "v"
Sleep 500ms

# Filter rows by what they show.
Type "f"
Sleep 400ms
Type "notes"
Enter
Sleep 1.6s
Escape
Sleep 900ms
Ctrl+Q
Sleep 600ms
