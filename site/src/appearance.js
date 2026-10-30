// Departure Mono with an ink-and-bone browser palette.
const family = 'Departure Mono';
const ink = '#111311';
const bone = '#e7e0cf';
// ANSI 220 is Airlock's accent; other ANSI text stays neutral.
const yellow = '#ffd700';

export function getFontSize() {
    return parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--terminal-fontSize'));
}

export const appearance = {
    fontFamily: `"${family}", "Airlock Mono", monospace`,
    fontSize: getFontSize(),
    cursorBlink: false,
    theme: {
        background: ink,
        foreground: bone,
        cursor: bone,
        cursorAccent: ink,
        selectionBackground: bone,
        selectionForeground: ink,
        black: ink,
        red: bone,
        green: bone,
        yellow,
        blue: bone,
        magenta: bone,
        cyan: bone,
        white: bone,
        brightBlack: bone,
        brightRed: bone,
        brightGreen: bone,
        brightYellow: yellow,
        brightBlue: bone,
        brightMagenta: bone,
        brightCyan: bone,
        brightWhite: bone,
    },
};

export async function loadFonts() {
    await document.fonts.load(`${appearance.fontSize}px "${family}"`);
}
