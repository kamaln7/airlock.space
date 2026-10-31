const status = document.getElementById('status');
const restart = document.getElementById('restart');

// The loader is the program's own first frame, drawn into the terminal before
// the program is there to draw it: the same cell, glyphs and colours as
// viewLoading in main.go, and the same clock - the moon's phase is wall time
// in eighths of a second, and the program carries on the page's wait from
// performance.timeOrigin - so the hand-over shows nothing at all.
const SPINNER_DELAY_MS = 500; // spinnerDelay in main.go
const SPINNER_STEP_MS = 125; // spinnerStep in main.go
const MOON = ['🌑', '🌒', '🌓', '🌔', '🌕', '🌖', '🌗', '🌘'];
const LABEL = 'loading...';
let stopLoader = () => {};

function startLoader(term) {
    let drawn = '';
    const draw = () => {
        if (performance.now() < SPINNER_DELAY_MS) return;
        const still = matchMedia('(prefers-reduced-motion: reduce)').matches;
        const moon = MOON[still ? 0 : Math.floor(Date.now() / SPINNER_STEP_MS) % MOON.length];
        // lipgloss centring: the odd cell goes on the right
        const row = Math.floor(term.rows / 2) + 1;
        const col = Math.floor((term.cols - (2 + 1 + LABEL.length)) / 2) + 1;
        const frame = `${row};${col};${moon}`;
        if (frame === drawn) return;
        // the alternate screen, as the program uses: it leaves the normal
        // screen clean for the goodbye
        const clear = drawn.startsWith(`${row};${col};`) ? '' : '\x1b[?1049h\x1b[?25l\x1b[2J';
        drawn = frame;
        term.write(`${clear}\x1b[${row};${col}H\x1b[38;5;220m${moon}\x1b[m \x1b[38;2;92;92;92m${LABEL}\x1b[m`);
    };
    draw();
    const timer = setInterval(draw, 25);
    stopLoader = (clear = false) => {
        clearInterval(timer);
        if (clear && drawn) term.write('\x1b[2J');
        stopLoader = () => {};
    };
}

restart.addEventListener('click', () => location.reload());

const copySsh = document.getElementById('copy-ssh');
const copyHint = document.getElementById('copy-hint');
const copyHintText = copyHint.textContent;
let copyHintTimer = 0;

document.addEventListener('pointerdown', (event) => {
    const selection = getSelection();
    if (selection && copySsh.contains(selection.anchorNode) && !copySsh.contains(event.target)) {
        selection.removeAllRanges();
    }
}, { capture: true });

copySsh.addEventListener('mousedown', (event) => {
    if (event.button === 0) event.preventDefault();
});

// Highlight the visible command with native selection, then copy that same text.
copySsh.addEventListener('click', async () => {
    copySsh.focus({ preventScroll: true });
    const command = copySsh.firstElementChild;
    getSelection().selectAllChildren(command);
    clearTimeout(copyHintTimer);
    try {
        await navigator.clipboard.writeText(command.textContent);
        copyHint.textContent = copyHintText;
        delete copyHint.dataset.state;
    } catch {
        copyHint.textContent = 'copy failed - press Cmd/Ctrl+C';
        copyHint.dataset.state = 'error';
        copyHintTimer = setTimeout(() => {
            copyHint.textContent = copyHintText;
            delete copyHint.dataset.state;
        }, 4000);
    }
});

let booba = null;
const terminalContainer = document.getElementById('terminal-container');
document.getElementById('terminal-frame').addEventListener('contextmenu', (event) => event.preventDefault());

document.addEventListener('keydown', (event) => {
    // Keep browser shortcuts out of the terminal without cancelling their defaults.
    if ((event.metaKey || event.ctrlKey) && ['r', 'l'].includes(event.key.toLowerCase())) {
        event.stopPropagation();
        return;
    }
    if (!booba?.adapter || terminalContainer.contains(event.target)) return;
    // A modifier alone must not steal focus or clear the header's text selection.
    if (['Alt', 'Control', 'Meta', 'Shift'].includes(event.key)) return;
    // Keep native focus navigation and activation of the header controls.
    if (event.target.closest?.('a, button') && ['Tab', 'Enter', ' '].includes(event.key)) return;
    // Keep native copying available while the header command is selected.
    if (event.target.closest?.('#copy-ssh') && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'c') return;

    // Let the terminal's own encoder handle modifiers, cursor modes, and repeats.
    const forwarded = new KeyboardEvent(event.type, event);
    booba.focus();
    terminalContainer.dispatchEvent(forwarded);
    if (forwarded.defaultPrevented) event.preventDefault();
}, { capture: true });

function setStatus(message, state = '') {
    status.textContent = message;
    status.dataset.state = state;
    restart.hidden = state === '';
}

async function start() {
    // Imported inside the caught startup so a missing or broken asset is shown.
    const [, { BoobaTerminal, OSC52Scanner }, { appearance, loadFonts, getFontSize }] = await Promise.all([
        import('../.generated/wasm_exec.js'),
        import('../.generated/booba/booba.js'),
        import('./appearance.js'),
    ]);

    const rootStyle = document.documentElement.style;
    for (const [key, value] of Object.entries(appearance.theme)) {
        rootStyle.setProperty(`--terminal-${key}`, value);
    }
    rootStyle.setProperty('--terminal-fontFamily', appearance.fontFamily);

    // Apply the CSS breakpoint before Booba's resize listener fits the cell grid.
    window.addEventListener('resize', () => {
        if (!booba?.term) return;
        const fontSize = getFontSize();
        if (booba.term.options.fontSize === fontSize) return;
        booba.options.fontSize = fontSize;
        booba.term.options.fontSize = fontSize;
    }, { capture: true });

    const go = new Go();

    // The terminal comes up while the program downloads, so the loader can be
    // drawn in it: the download is the long wait on a first visit.
    booba = new BoobaTerminal('terminal-container', {
        ...appearance,
        cursorVisible: false,
        disableSelection: true,
    });
    const terminal = loadFonts().then(() => booba.init()).then(() => startLoader(booba));
    const program = fetch(new URL('../.generated/app.wasm', import.meta.url)).then((response) => {
        if (!response.ok) {
            throw new Error(`app.wasm: HTTP ${response.status} ${response.statusText}`);
        }
        return WebAssembly.instantiateStreaming(response, go.importObject);
    });
    const [{ instance }] = await Promise.all([program, terminal]);

    // Go consumes these: it reads the size from the terminal and renders
    // through airlockWrite, so clipboard sequences are scanned here.
    const clipboard = new OSC52Scanner(true);
    window.airlockTerminal = booba;
    window.airlockWrite = (data) => {
        clipboard.scan(data);
        booba.write(data);
    };

    // Go's main runs synchronously up to its first yield; connect before
    // awaiting. The loader stops without clearing: the program's first frame
    // replaces it, carrying on the same wait and the same moon.
    stopLoader();
    const exited = go.run(instance);
    booba.connectWasm();
    booba.focus();
    setStatus('');

    const exitCode = await exited;
    // The program is gone: stop polling it. Its goodbye is already on screen.
    booba.disconnect();
    if (exitCode !== 0) {
        throw new Error(`Airlock exited with code ${exitCode}`);
    }
    setStatus('Session ended.', 'finished');
}

start().catch((err) => {
    console.error(err);
    stopLoader(true);
    booba?.disconnect();
    setStatus(err instanceof Error ? err.message : String(err), 'error');
});

window.addEventListener('pagehide', () => booba?.dispose());
window.addEventListener('pageshow', (event) => {
    // pagehide disposed the terminal, so a bfcache restore would be dead.
    if (event.persisted) location.reload();
});
