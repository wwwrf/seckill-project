// Standard benchmark: application-layer hot read throughput (RPS/QPS)
// Focuses on product/activity read endpoints that benefit from cache layers.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { cfgNum, cfgStr, envNum, envStr, pickConfig } from './lib/common.js';

const cfg = pickConfig('app_read');
const BASE_URL = envStr('BASE_URL', cfgStr(cfg, 'base_url', 'http://127.0.0.1:8080'));
const PRODUCT_ID = envNum('PRODUCT_ID', cfgNum(cfg, 'product_id', 1));
const ACTIVITY_ID = envNum('ACTIVITY_ID', cfgNum(cfg, 'activity_id', 1));

const readLatency = new Trend('app_read_latency_ms');
const readOK = new Counter('app_read_ok');
const readErr = new Counter('app_read_err');

export const options = {
    scenarios: {
        hot_read: {
            executor: 'constant-arrival-rate',
            rate: envNum('TARGET_RPS', cfgNum(cfg, 'target_rps', 1200)),
            timeUnit: '1s',
            duration: envStr('DURATION', cfgStr(cfg, 'duration', '2m')),
            preAllocatedVUs: envNum('PRE_VUS', cfgNum(cfg, 'pre_vus', 200)),
            maxVUs: envNum('MAX_VUS', cfgNum(cfg, 'max_vus', 1200)),
        },
    },
    thresholds: {
        http_req_duration: [`p(95)<${cfgNum(cfg, 'http_p95_ms', 200)}`, `p(99)<${cfgNum(cfg, 'http_p99_ms', 400)}`],
        http_req_failed: [`rate<${cfgNum(cfg, 'http_error_rate', 0.02)}`],
        app_read_latency_ms: [`p(95)<${cfgNum(cfg, 'http_p95_ms', 200)}`, `p(99)<${cfgNum(cfg, 'http_p99_ms', 400)}`],
    },
};

export default function () {
    const pickProduct = Math.random() < 0.6;
    const path = pickProduct ? `/api/v1/product/${PRODUCT_ID}` : `/api/v1/activity/${ACTIVITY_ID}`;

    const start = Date.now();
    const res = http.get(`${BASE_URL}${path}`, { tags: { endpoint: pickProduct ? 'product' : 'activity' } });
    readLatency.add(Date.now() - start);

    const ok = check(res, {
        'http 200': (r) => r.status === 200,
    });

    if (ok) {
        readOK.add(1);
    } else {
        readErr.add(1);
    }

    sleep(0.001);
}

export function handleSummary(data) {
    const m = data.metrics;
    const ok = m.app_read_ok ? m.app_read_ok.values.count : 0;
    const err = m.app_read_err ? m.app_read_err.values.count : 0;
    const total = ok + err;
    const errRate = total > 0 ? ((err * 100) / total).toFixed(2) + '%' : 'N/A';

    const rps = m.http_reqs && m.http_reqs.values ? m.http_reqs.values.rate : 0;

    const p95 = m.app_read_latency_ms && m.app_read_latency_ms.values && m.app_read_latency_ms.values['p(95)'] != null
        ? Number(m.app_read_latency_ms.values['p(95)']).toFixed(2)
        : 'N/A';
    const p99 = m.app_read_latency_ms && m.app_read_latency_ms.values && m.app_read_latency_ms.values['p(99)'] != null
        ? Number(m.app_read_latency_ms.values['p(99)']).toFixed(2)
        : 'N/A';

    console.log('\n========== STANDARD BENCHMARK: APP HOT READ ==========');
    console.log(`RPS:            ${rps.toFixed ? rps.toFixed(2) : rps}`);
    console.log(`QPS:            ${rps.toFixed ? rps.toFixed(2) : rps}`);
    console.log('TPS:            N/A (read benchmark)');
    console.log(`P95(ms):        ${p95}`);
    console.log(`P99(ms):        ${p99}`);
    console.log(`Error Rate:     ${errRate}`);
    console.log(`Read OK:        ${ok}`);
    console.log(`Read Error:     ${err}`);
    console.log('======================================================\n');

    return {};
}
