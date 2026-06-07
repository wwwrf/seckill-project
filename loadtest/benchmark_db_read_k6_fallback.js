// Standard benchmark fallback: DB-sensitive read path over HTTP (plain k6 only)
// This is NOT direct MySQL benchmarking. It approximates DB read pressure via API.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { authHeaders, cfgNum, cfgStr, envNum, envStr, loadUsers, pickConfig, pickUser } from './lib/common.js';

const cfg = pickConfig('db_read_fallback');
const BASE_URL = envStr('BASE_URL', cfgStr(cfg, 'base_url', 'http://127.0.0.1:8080'));
const usersData = loadUsers('../loadtest/users_token.csv');

const readLatency = new Trend('db_fallback_read_latency_ms');
const readOk = new Counter('db_fallback_read_ok');
const readErr = new Counter('db_fallback_read_err');

export const options = {
    scenarios: {
        read_via_api: {
            executor: 'constant-arrival-rate',
            rate: envNum('TARGET_RPS', cfgNum(cfg, 'target_rps', 900)),
            timeUnit: '1s',
            duration: envStr('DURATION', cfgStr(cfg, 'duration', '2m')),
            preAllocatedVUs: envNum('PRE_VUS', cfgNum(cfg, 'pre_vus', 150)),
            maxVUs: envNum('MAX_VUS', cfgNum(cfg, 'max_vus', 900)),
        },
    },
    thresholds: {
        http_req_duration: [`p(95)<${cfgNum(cfg, 'http_p95_ms', 600)}`, `p(99)<${cfgNum(cfg, 'http_p99_ms', 1200)}`],
        http_req_failed: [`rate<${cfgNum(cfg, 'http_error_rate', 0.03)}`],
    },
};

export default function () {
    const user = pickUser(usersData);
    const headers = authHeaders(user.token);
    const page = Math.floor(Math.random() * 10) + 1;

    const start = Date.now();
    const res = http.get(`${BASE_URL}/api/v1/orders?page=${page}&page_size=10`, { headers, tags: { endpoint: 'orders' } });
    readLatency.add(Date.now() - start);

    const ok = check(res, {
        'orders http 200': (r) => r.status === 200,
    });

    if (ok) {
        readOk.add(1);
    } else {
        readErr.add(1);
    }

    sleep(0.002);
}

export function handleSummary(data) {
    const m = data.metrics;
    const ok = m.db_fallback_read_ok ? m.db_fallback_read_ok.values.count : 0;
    const err = m.db_fallback_read_err ? m.db_fallback_read_err.values.count : 0;
    const total = ok + err;
    const errRate = total > 0 ? ((err * 100) / total).toFixed(2) + '%' : 'N/A';
    const rps = m.http_reqs && m.http_reqs.values ? m.http_reqs.values.rate : 0;

    console.log('\n========== STANDARD BENCHMARK: DB READ FALLBACK (k6) ==========');
    console.log(`RPS:            ${rps.toFixed ? rps.toFixed(2) : rps}`);
    console.log(`QPS:            ${rps.toFixed ? rps.toFixed(2) : rps}`);
    console.log('TPS:            N/A (read benchmark)');
    console.log(`P95(ms):        ${m.db_fallback_read_latency_ms ? m.db_fallback_read_latency_ms.values['p(95)'].toFixed(2) : 'N/A'}`);
    console.log(`P99(ms):        ${m.db_fallback_read_latency_ms ? m.db_fallback_read_latency_ms.values['p(99)'].toFixed(2) : 'N/A'}`);
    console.log(`Error Rate:     ${errRate}`);
    console.log(`Read OK:        ${ok}`);
    console.log(`Read Error:     ${err}`);
    console.log('NOTE: This mode is API-level DB-sensitive read, not direct MySQL benchmark.');
    console.log('==============================================================\n');

    return {};
}
