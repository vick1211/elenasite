// ===== Переключение вкладок =====
function switchTab(event, tabId) {
    document.querySelectorAll('.admin-tab').forEach(tab => tab.classList.remove('active'));
    document.querySelectorAll('.tab-content').forEach(content => content.classList.remove('active'));
    event.target.classList.add('active');
    document.getElementById(tabId).classList.add('active');

    if (tabId === 'bookings' && !bookingsLoaded) loadBookings();
}

// ===== Общие хелперы для работы с API =====
const API_BASE = '/api/v1';

async function apiFetch(path, options = {}) {
    const token = sessionStorage.getItem('adminToken');
    const headers = Object.assign(
        { 'Content-Type': 'application/json' },
        options.headers || {},
        token ? { 'Authorization': 'Bearer ' + token } : {}
    );

    const response = await fetch(API_BASE + path, Object.assign({}, options, { headers }));

    if (response.status === 401) {
        sessionStorage.clear();
        window.location.href = '/admin/';
        throw new Error('Сессия истекла');
    }

    const contentType = response.headers.get('content-type') || '';
    const isJson = contentType.includes('application/json');
    const data = isJson ? await response.json() : null;

    if (!response.ok) {
        throw new Error((data && data.error) || 'Ошибка запроса');
    }

    return data;
}

function escapeHtml(str) {
    const div = document.createElement('div');
    div.textContent = str === undefined || str === null ? '' : str;
    return div.innerHTML;
}

function formatDateTime(iso) {
    const d = new Date(iso);
    const datePart = d.toLocaleDateString('ru-RU', { day: 'numeric', month: 'long', year: 'numeric' });
    const timePart = d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
    return { datePart, timePart };
}

const STATUS_LABELS = {
    pending:   { text: 'Ожидает оплаты',   cls: 'status-pending' },
    confirmed: { text: 'Подтверждено',     cls: 'status-confirmed' },
    cancelled: { text: 'Отменено',         cls: 'status-cancelled' },
    completed: { text: 'Завершено',        cls: 'status-confirmed' },
    no_show:   { text: 'Клиент не пришёл', cls: 'status-cancelled' },
};

const PAYMENT_LABELS = {
    pending:  'оплата ожидается',
    paid:     'оплачено',
    failed:   'оплата не прошла',
    refunded: 'возврат оформлен',
};

// ===== ЗАПИСИ КЛИЕНТОВ =====
let bookingsLoaded = false;

async function loadBookings() {
    const tbody = document.getElementById('bookingsTableBody');
    tbody.innerHTML = '<tr><td colspan="6">Загрузка…</td></tr>';

    try {
        const list = await apiFetch('/admin/appointments?limit=200');
        bookingsLoaded = true;
        renderBookings(list || []);
    } catch (err) {
        tbody.innerHTML = `<tr><td colspan="6">Не удалось загрузить записи: ${escapeHtml(err.message)}</td></tr>`;
    }
}

function renderBookings(list) {
    const tbody = document.getElementById('bookingsTableBody');

    if (list.length === 0) {
        tbody.innerHTML = '<tr><td colspan="6">Пока нет ни одной записи</td></tr>';
        return;
    }

    tbody.innerHTML = list.map(appt => {
        const client = appt.client || {};
        const service = appt.service || {};
        const { datePart, timePart } = formatDateTime(appt.starts_at);
        const statusInfo = STATUS_LABELS[appt.status] || { text: appt.status, cls: 'status-pending' };
        const paymentLabel = PAYMENT_LABELS[appt.payment_status] || appt.payment_status;
        const canAct = appt.status === 'pending' || appt.status === 'confirmed';

        return `
            <tr data-id="${appt.id}">
                <td>
                    <strong>${escapeHtml(client.first_name)} ${escapeHtml(client.last_name)}</strong><br>
                    <span style="font-size: 12px; color: var(--text-muted);">${escapeHtml(client.phone)} · ${escapeHtml(client.email)}</span>
                </td>
                <td>${escapeHtml(service.title)}</td>
                <td>${datePart}<br>${timePart}</td>
                <td>${appt.format === 'online' ? 'Онлайн' : 'Очно'}</td>
                <td>
                    <span class="status ${statusInfo.cls}">${statusInfo.text}</span><br>
                    <span style="font-size: 11px; color: var(--text-muted);">${escapeHtml(paymentLabel)}</span>
                </td>
                <td>
                    ${canAct ? `<button class="btn btn-small" onclick="rescheduleBooking('${appt.id}')">Перенести</button>` : ''}
                    ${canAct ? `<button class="btn btn-small btn-danger" onclick="cancelBooking('${appt.id}')">Отменить</button>` : ''}
                    <button class="btn btn-small btn-danger" onclick="deleteBooking('${appt.id}')">Удалить</button>
                </td>
            </tr>
        `;
    }).join('');
}

async function cancelBooking(id) {
    if (!confirm('Отменить эту запись? Клиент не будет уведомлён автоматически.')) return;
    try {
        await apiFetch(`/admin/appointments/${id}/status`, {
            method: 'PATCH',
            body: JSON.stringify({ status: 'cancelled' }),
        });
        loadBookings();
    } catch (err) {
        alert('Не удалось отменить запись: ' + err.message);
    }
}

async function deleteBooking(id) {
    if (!confirm('Удалить запись безвозвратно? Это действие нельзя отменить.')) return;
    try {
        await apiFetch(`/admin/appointments/${id}`, { method: 'DELETE' });
        loadBookings();
    } catch (err) {
        alert('Не удалось удалить запись: ' + err.message);
    }
}

async function rescheduleBooking(id) {
    const input = prompt('Новая дата и время (в формате ГГГГ-ММ-ДД ЧЧ:ММ), например 2026-07-25 14:00:');
    if (!input) return;

    const match = input.trim().match(/^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})$/);
    if (!match) {
        alert('Неверный формат. Пример: 2026-07-25 14:00');
        return;
    }
    const [, y, mo, d, h, mi] = match;
    const startsAt = new Date(Number(y), Number(mo) - 1, Number(d), Number(h), Number(mi)).toISOString();

    try {
        await apiFetch(`/admin/appointments/${id}/reschedule`, {
            method: 'PATCH',
            body: JSON.stringify({ starts_at: startsAt }),
        });
        loadBookings();
    } catch (err) {
        alert('Не удалось перенести запись: ' + err.message);
    }
}


// ===== Ручное создание записи (клиент договорился по телефону и т.п.) =====
let servicesForSelect = [];

async function ensureServiceOptionsLoaded() {
    if (servicesForSelect.length > 0) return;
    try {
        const response = await fetch('/api/v1/services');
        servicesForSelect = await response.json() || [];
        const select = document.getElementById('manualServiceSelect');
        select.innerHTML = servicesForSelect
            .map(s => `<option value="${s.id}">${escapeHtml(s.title)} — ${(s.price_kopeks / 100).toLocaleString('ru-RU')} ₽</option>`)
            .join('');
    } catch (err) {
        console.error('Не удалось загрузить список услуг:', err);
    }
}

async function toggleManualBookingForm() {
    const form = document.getElementById('manualBookingForm');
    const isHidden = form.style.display === 'none' || !form.style.display;
    form.style.display = isHidden ? 'block' : 'none';
    if (isHidden) {
        await ensureServiceOptionsLoaded();
        document.getElementById('manualFirstName').focus();
    } else {
        clearManualBookingForm();
    }
}

function clearManualBookingForm() {
    ['manualFirstName', 'manualLastName', 'manualPatronym', 'manualPhone', 'manualEmail', 'manualDate', 'manualTime', 'manualNotes']
        .forEach(id => { document.getElementById(id).value = ''; });
}

async function saveManualBooking() {
    const firstName = document.getElementById('manualFirstName').value.trim();
    const lastName = document.getElementById('manualLastName').value.trim();
    const patronym = document.getElementById('manualPatronym').value.trim();
    const phone = document.getElementById('manualPhone').value.trim();
    const email = document.getElementById('manualEmail').value.trim();
    const serviceId = document.getElementById('manualServiceSelect').value;
    const dateStr = document.getElementById('manualDate').value;
    const timeStr = document.getElementById('manualTime').value;
    const format = document.getElementById('manualFormat').value;
    const paymentStatus = document.getElementById('manualPaymentStatus').value;
    const notes = document.getElementById('manualNotes').value.trim();

    if (!firstName || !lastName || !phone || !email || !serviceId || !dateStr || !timeStr) {
        alert('Заполните обязательные поля: имя, фамилия, телефон, email, услуга, дата и время');
        return;
    }

    const [y, mo, d] = dateStr.split('-').map(Number);
    const [hh, mi] = timeStr.split(':').map(Number);
    const startsAt = new Date(y, mo - 1, d, hh, mi, 0).toISOString();

    try {
        await apiFetch('/admin/appointments', {
            method: 'POST',
            body: JSON.stringify({
                client: { first_name: firstName, last_name: lastName, patronym, phone, email },
                service_id: serviceId,
                format,
                starts_at: startsAt,
                payment_status: paymentStatus,
                notes,
            }),
        });
        clearManualBookingForm();
        toggleManualBookingForm();
        alert('Запись создана, клиенту отправлено письмо с подтверждением');
        loadBookings();
    } catch (err) {
        alert('Не удалось создать запись: ' + err.message);
    }
}