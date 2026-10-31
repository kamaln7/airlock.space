const status = document.getElementById('status');
const restart = document.getElementById('restart');

// The loader is the program's own first frame, drawn into the terminal before
// the program is there to draw it: the same cell and colours as viewLoading in
// main.go, and the same clock - the moon's phase is wall time in eighths of a
// second, and the program carries on the page's wait from
// performance.timeOrigin - so the hand-over shows nothing at all. Where the
// program will say "loading...", the page shows how much of the program has
// arrived, in a bar exactly as wide.
//
// The bar is Bubbles' progress bar (charm.land/bubbles/v2/progress) in this
// palette: a fill blended across the bar's width, a ░ track, and the shown
// fraction chasing the real one on its critically damped spring (angular
// frequency 18, damping ratio 1) rather than jumping with each chunk. Its
// edge advances in eighths of a cell, so the motion is smooth even ten cells
// wide.
const SPINNER_DELAY_MS = 500; // spinnerDelay in main.go
const SPINNER_STEP_MS = 125; // spinnerStep in main.go
const MOON = ['🌑', '🌒', '🌓', '🌔', '🌕', '🌖', '🌗', '🌘'];
const LABEL = 'loading...'; // the program's, which the bar stands in for
const BAR_CELLS = LABEL.length;
const EIGHTHS = ['', '▏', '▎', '▍', '▌', '▋', '▊', '▉'];
const BLEND_FROM = [0x5c, 0x5c, 0x5c]; // colorMuted (dark) in main.go
const BLEND_TO = [0xff, 0xd7, 0x00]; // ANSI 220, colorYellow in main.go
const TRACK = '38;2;60;60;60'; // colorSuperMuted (dark) in main.go
const SPRING_FREQUENCY = 18;
let downloaded = 0; // the fraction of the program in hand
let stopLoader = () => {};

function blendAt(t) {
    return BLEND_FROM.map((from, i) => Math.round(from + (BLEND_TO[i] - from) * t)).join(';');
}

// Full cells are Bubbles' half block, its left half and right half each their
// own point on the blend, so the gradient runs in twice the steps of the cells.
function progressBar(fraction) {
    const eighths = Math.round(Math.max(0, Math.min(1, fraction)) * BAR_CELLS * 8);
    const full = Math.floor(eighths / 8);
    const at = (half) => blendAt(half / (2 * BAR_CELLS - 1));
    let bar = '';
    for (let i = 0; i < full; i++) bar += `\x1b[38;2;${at(2 * i)};48;2;${at(2 * i + 1)}m▌`;
    let cells = full;
    if (eighths % 8) {
        bar += `\x1b[49;38;2;${at(2 * full)}m${EIGHTHS[eighths % 8]}`;
        cells++;
    }
    return `${bar}\x1b[49;${TRACK}m${'░'.repeat(BAR_CELLS - cells)}\x1b[m`;
}

function startLoader(term) {
    let drawn = '';
    let shown = 0;
    let velocity = 0;
    let last = performance.now();
    let frame = 0;
    const draw = (now) => {
        frame = requestAnimationFrame(draw);
        const still = matchMedia('(prefers-reduced-motion: reduce)').matches;
        // the spring, stepped by the frame's own time so a slow frame does
        // not slow the bar; capped so a backgrounded tab does not lurch
        const dt = Math.min(0.05, (now - last) / 1000);
        last = now;
        if (still) {
            shown = downloaded;
        } else {
            const w = SPRING_FREQUENCY;
            velocity += (w * w * (downloaded - shown) - 2 * w * velocity) * dt;
            shown += velocity * dt;
        }
        if (now < SPINNER_DELAY_MS) return;
        const moon = MOON[still ? 0 : Math.floor(Date.now() / SPINNER_STEP_MS) % MOON.length];
        // lipgloss centring: the odd cell goes on the right
        const row = Math.floor(term.rows / 2) + 1;
        const col = Math.floor((term.cols - (2 + 1 + LABEL.length)) / 2) + 1;
        const bar = progressBar(shown);
        const next = `${row};${col};${moon};${bar}`;
        if (next === drawn) return;
        // the alternate screen, as the program uses: it leaves the normal
        // screen clean for the goodbye
        const clear = drawn.startsWith(`${row};${col};`) ? '' : '\x1b[?1049h\x1b[?25l\x1b[2J';
        drawn = next;
        term.write(`${clear}\x1b[${row};${col}H\x1b[38;5;220m${moon}\x1b[m ${bar}`);
    };
    frame = requestAnimationFrame(draw);
    stopLoader = (clear = false) => {
        cancelAnimationFrame(frame);
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
    // Counted as the bytes come out of the response, which is after the
    // browser has decompressed them, against the program's own size from the
    // build: Content-Length is the compressed size, or absent when the server
    // compresses on the fly.
    const program = Promise.all([
        fetch(new URL('../.generated/app.wasm', import.meta.url)),
        import('../.generated/app-wasm-size.json'),
    ]).then(([response, { default: size }]) => {
        if (!response.ok) {
            throw new Error(`app.wasm: HTTP ${response.status} ${response.statusText}`);
        }
        let got = 0;
        const counted = response.body.pipeThrough(new TransformStream({
            transform(chunk, controller) {
                got += chunk.byteLength;
                downloaded = got / size.bytes;
                controller.enqueue(chunk);
            },
        }));
        return WebAssembly.instantiateStreaming(new Response(counted, { headers: response.headers }), go.importObject);
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
    // the goodbye is on screen; the restart button is all the page adds
    setStatus('', 'finished');
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
