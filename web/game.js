const telegram = window.Telegram?.WebApp;
const ticket = new URLSearchParams(location.search).get("t");
const board = document.querySelector("#board");
const status = document.querySelector("#status");
const symbol = document.querySelector("#symbol");
const again = document.querySelector("#again");
const marks = ["", "×", "○"];
let state;
let busy = false;
let stopped = false;

const cells = Array.from({ length: 9 }, (_, index) => {
  const cell = document.createElement("button");
  cell.type = "button";
  cell.className = "cell";
  cell.disabled = true;
  cell.setAttribute("aria-label", `Клетка ${index + 1}`);
  cell.addEventListener("click", () => request("move", index));
  board.append(cell);
  return cell;
});

function render() {
  const canMove = !state.finished && state.turn === state.you;
  symbol.textContent = `Ты ${marks[state.you]}`;
  symbol.hidden = false;
  if (state.finished) {
    status.textContent = state.winner === 0 ? "Ничья" : state.winner === state.you ? "Ты выиграл!" : "Победа соперника";
  } else {
    status.textContent = canMove ? "Твой ход" : "Ход соперника";
  }
  cells.forEach((cell, index) => {
    const mark = state.board[index];
    cell.textContent = marks[mark];
    cell.dataset.mark = String(mark);
    cell.disabled = busy || !canMove || mark !== 0;
    cell.setAttribute("aria-label", `Клетка ${index + 1}${mark ? `, ${marks[mark]}` : ""}`);
  });
  again.hidden = !state.finished;
  again.disabled = busy;
}

function fail(message, permanent) {
  status.textContent = message;
  stopped = permanent;
  cells.forEach(cell => { cell.disabled = true; });
  again.hidden = true;
}

async function request(action = "state", cell) {
  if (busy || stopped) return;
  busy = true;
  if (action !== "state" && state) render();
  try {
    const response = await fetch("/game/api", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ initData: telegram.initData, ticket, action, cell, version: state?.version }),
      signal: AbortSignal.timeout(8000),
    });
    if (response.ok) {
      state = await response.json();
      busy = false;
      render();
    } else if (response.status === 409) {
      busy = false;
      await request();
    } else if (response.status === 401) {
      fail("Открой игру заново из Telegram.", true);
    } else if (response.status === 404) {
      fail("Эта партия уже недоступна.", true);
    } else {
      fail("Связь прервалась. Пробуем ещё раз.", false);
    }
  } catch {
    fail("Связь прервалась. Пробуем ещё раз.", false);
  } finally {
    busy = false;
  }
}

again.addEventListener("click", () => request("reset"));
if (telegram?.initData && ticket) {
  telegram.ready();
  request();
  setInterval(() => { if (!document.hidden) request(); }, 2000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) request(); });
} else {
  fail("Открой игру из темы в Telegram.", true);
}
