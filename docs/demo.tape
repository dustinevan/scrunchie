# The README demo. Record it with `make gif` (needs Charm's VHS).
# It runs the offline --demo workspace, so no database is involved, and it
# points HOME at a throwaway directory so saved connections never appear.
# Everything after launch uses keys people bring from other tools.

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
Set TypingSpeed 60ms
Set Theme { "name": "Plum Night", "background": "#1a1026", "foreground": "#f4eafc", "cursor": "#ff3ea5", "selection": "#5b2386", "black": "#1a1026", "red": "#ff5f5f", "green": "#05ffa1", "yellow": "#ffe45e", "blue": "#01cdfe", "magenta": "#ff71ce", "cyan": "#01cdfe", "white": "#f4eafc", "brightBlack": "#4b3470", "brightRed": "#ff5f5f", "brightGreen": "#05ffa1", "brightYellow": "#ffe45e", "brightBlue": "#01cdfe", "brightMagenta": "#ff71ce", "brightCyan": "#01cdfe", "brightWhite": "#ffffff" }

Hide
Type "export HOME=$PWD/.scratch/vhs-home COLORTERM=truecolor PATH=$PWD/bin:$PATH; clear"
Enter
Show

Type "scrunch --demo"
Sleep 200ms
Enter
Sleep 1.4s

# Run, then move the way vim does.
Ctrl+J
Sleep 900ms
Type@220ms "jjj"
Type@220ms "kkk"
Type@220ms "ll"
Sleep 400ms

# Open the JSON, unfold it with j and l, back out with q.
Enter
Sleep 700ms
Type@160ms "jjjj"
Type "l"
Sleep 400ms
Type@160ms "jj"
Type "l"
Sleep 1.4s
Type "q"
Sleep 500ms

# Same bytes, many views.
Type "l"
Sleep 400ms
Type@450ms "vvvvv"
Sleep 900ms
Type "v"
Sleep 400ms

# Ctrl+D means different things in different tools, so it won't guess.
Ctrl+D
Sleep 2s

# ? shows what you can do right here.
Type "?"
Sleep 2.4s
Type "q"
Sleep 400ms

# Over to the database tree and just type a name.
Type@150ms "hhhh"
Sleep 400ms
Type@200ms "act"
Sleep 1.6s
Escape
Sleep 500ms
Ctrl+Q
Sleep 400ms
