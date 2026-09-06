/* Forge Depot — a deliberately small, dependency-free demo target. export-denylist: ok
 * Four views (login → orders → detail → confirmation) give a spec something
 * real to traverse, and give a breakage something real to break.
 *
 * Forge Depot is a fictional store that exists only for this fixture. export-denylist: ok */

const ORDERS = [
  {
    id: "SO-4471",
    customer: "Northwind Traders",
    total: "$12,480.00",
    items: 14,
  },
  { id: "SO-4472", customer: "Contoso Rail", total: "$3,905.50", items: 6 },
  {
    id: "SO-4473",
    customer: "Fabrikam Metals",
    total: "$27,310.25",
    items: 31,
  },
];

const views = ["login", "orders", "detail", "done"];

function show(name) {
  for (const view of views) {
    document.getElementById(`view-${view}`).hidden = view !== name;
  }
}

function renderOrders() {
  const list = document.getElementById("order-list");
  list.replaceChildren();
  for (const order of ORDERS) {
    const row = document.createElement("button");
    row.type = "button";
    row.className = "order-row";
    row.dataset.orderId = order.id;
    row.innerHTML =
      `<span class="order-id">${order.id}</span>` +
      `<span class="order-customer">${order.customer}</span>` +
      `<span class="order-total">${order.total}</span>`;
    row.addEventListener("click", () => openDetail(order));
    const li = document.createElement("li");
    li.append(row);
    list.append(li);
  }
}

function openDetail(order) {
  document.getElementById("detail-title").textContent =
    `${order.customer} — ${order.items} items`;
  document.querySelector('[data-testid="detail-id"]').textContent = order.id;
  document.querySelector('[data-testid="detail-customer"]').textContent =
    order.customer;
  document.querySelector('[data-testid="detail-total"]').textContent =
    order.total;
  document.getElementById("note").value = "";
  show("detail");
}

document.getElementById("login-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const email = document.getElementById("email").value.trim();
  const password = document.getElementById("password").value;
  const error = document.getElementById("login-error");
  if (!email || password.length < 4) {
    error.hidden = false;
    return;
  }
  error.hidden = true;
  document.querySelector('[data-testid="current-user"]').textContent = email;
  renderOrders();
  show("orders");
});

document.getElementById("approve-button").addEventListener("click", () => {
  const orderId = document.querySelector(
    '[data-testid="detail-id"]',
  ).textContent;
  const note = document.getElementById("note").value.trim();
  document.getElementById("confirmation-detail").textContent =
    `${orderId} approved${note ? ` — "${note}"` : ""}.`;
  show("done");
});

document.addEventListener("click", (event) => {
  const action = event.target.dataset?.action;
  if (action === "back") show("orders");
  if (action === "restart") show("orders");
});

show("login");
