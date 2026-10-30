const status = document.getElementById('status');
const restart = document.getElementById('restart');
const loading = document.getElementById('loading');
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
    loading.hidden = true;
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

    const response = await fetch(new URL('../.generated/app.wasm', import.meta.url));
    if (!response.ok) {
        throw new Error(`app.wasm: HTTP ${response.status} ${response.statusText}`);
    }

    booba = new BoobaTerminal('terminal-container', {
        ...appearance,
        cursorVisible: false,
        disableSelection: true,
    });
    const [{ instance }] = await Promise.all([
        WebAssembly.instantiateStreaming(response, go.importObject),
        loadFonts().then(() => booba.init()),
    ]);

    // Go consumes these: it reads the size from the terminal and renders
    // through airlockWrite, so clipboard sequences are scanned here.
    const clipboard = new OSC52Scanner(true);
    window.airlockTerminal = booba;
    window.airlockWrite = (data) => {
        clipboard.scan(data);
        booba.write(data);
    };

    // Go's main runs synchronously up to its first yield; connect before awaiting.
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
    booba?.disconnect();
    setStatus(err instanceof Error ? err.message : String(err), 'error');
});

window.addEventListener('pagehide', () => booba?.dispose());
window.addEventListener('pageshow', (event) => {
    // pagehide disposed the terminal, so a bfcache restore would be dead.
    if (event.persisted) location.reload();
});
