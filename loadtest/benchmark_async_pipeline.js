import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { Counter, Trend } from 'k6/metrics';
import { authHeaders, cfgNum, cfgStr, envNum, envStr, loadUsers, pickConfig, safeJsonParse } from './lib/common.js';

const cfg = pickConfig('seckill_submit');
const BASE_URL = envStr('BASE_URL', cfgStr(cfg, 'base_url', 'http://127.0.0.1:8080'));
const ACTIVITY_ID = envNum('ACTIVITY_ID', cfgNum(cfg, 'activity_id', 1));
const PRODUCT_ID = envNum('PRODUCT_ID', cfgNum(cfg, 'product_id', 1));
const ADMIN_TOKEN = envStr('ADMIN_TOKEN', 'change-this-admin-token-in-production');
const usersData = loadUsers('../loadtest/users_token.csv');

const acceptedCounter = new Counter('async_submit_accepted');
const errorCounter = new Counter('async_submit_error');
const submitLatency = new Trend('async_submit_latency_ms');
const createdTotalTrend = new Trend('async_created_total');
const backlogTrend = new Trend('async_backlog_pending');
const avgCreateLatencyTrend = new Trend('async_avg_create_latency_ms');

export const options = {
    scenarios: {
        submit: {
            executor: 'ramping-arrival-rate',
            startRate: envNum('START_RPS', cfgNum(cfg, 'start_rps', 100)),
            timeUnit: '1s',
            preAllocatedVUs: envNum('PRE_VUS', cfgNum(cfg, 'pre_vus', 300)),
            maxVUs: envNum('MAX_VUS', cfgNum(cfg, 'max_vus', 2000)),
            stages: [
                { target: envNum('RPS_STAGE_1', cfgNum(cfg, 'rps_stage_1', 300)), duration: envStr('STAGE_1', cfgStr(cfg, 'stage_1', '30s')) },
                { target: envNum('RPS_STAGE_2', cfgNum(cfg, 'rps_stage_2', 600)), duration: envStr('STAGE_2', cfgStr(cfg, 'stage_2', '30s')) },
                { target: envNum('RPS_STAGE_3', cfgNum(cfg, 'rps_stage_3', 900)), duration: envStr('STAGE_3', cfgStr(cfg, 'stage_3', '30s')) },
                { target: envNum('RPS_STAGE_4', cfgNum(cfg, 'rps_stage_4', 1200)), duration: envStr('STAGE_4', cfgStr(cfg, 'stage_4', '30s')) },
            ],
        },
        metrics: {
            executor: 'constant-vus',
            vus: 1,
            duration: '2m5s',
            exec: 'sampleMetrics',
        },
    },
};

export default function () {
    const user = usersData[exec.scenario.iterationInTest % usersData.length];
    const headers = authHeaders(user.token);
    const start = Date.now();
    const res = http.post(`${BASE_URL}/api/v1/seckill`, JSON.stringify({ activity_id: ACTIVITY_ID, product_id: PRODUCT_ID }), { headers });
    submitLatency.add(Date.now() - start);

    const ok = check(res, { 'submit http 200': (r) => r.status === 200 });
    if (!ok) {
        errorCounter.add(1);
        return;
    }

    const body = safeJsonParse(res.body);
    if (!body) {
        errorCounter.add(1);
        return;
    }

    if (body.code === 0) {
        acceptedCounter.add(1);
    } else {
        errorCounter.add(1);
    }

    sleep(0.001);
}

export function sampleMetrics() {
    const res = http.get(
        `${BASE_URL}/api/v1/admin/benchmark/metrics?activity_id=${ACTIVITY_ID}&product_id=${PRODUCT_ID}`,
        { headers: { 'X-Admin-Token': ADMIN_TOKEN } },
    );
    if (res.status !== 200) {
        sleep(1);
        return;
    }

    const body = safeJsonParse(res.body);
    if (!body || body.code !== 0 || !body.data) {
        sleep(1);
        return;
    }

    createdTotalTrend.add(body.data.created_total || 0);
    backlogTrend.add(body.data.backlog_pending || 0);
    avgCreateLatencyTrend.add(body.data.avg_create_latency_ms || 0);
    sleep(1);
}

export function handleSummary(data) {
    const m = data.metrics;
    const submitRps = m.http_reqs && m.http_reqs.values ? m.http_reqs.values.rate : 0;
    const acceptedTps = m.async_submit_accepted && m.async_submit_accepted.values ? m.async_submit_accepted.values.rate : 0;
    const submitP95 = m.async_submit_latency_ms && m.async_submit_latency_ms.values && m.async_submit_latency_ms.values['p(95)'] != null
        ? Number(m.async_submit_latency_ms.values['p(95)']).toFixed(2)
        : 'N/A';
    const backlogP95 = m.async_backlog_pending && m.async_backlog_pending.values && m.async_backlog_pending.values['p(95)'] != null
        ? Number(m.async_backlog_pending.values['p(95)']).toFixed(2)
        : 'N/A';
    const avgCreateLatency = m.async_avg_create_latency_ms && m.async_avg_create_latency_ms.values && m.async_avg_create_latency_ms.values.avg != null
        ? Number(m.async_avg_create_latency_ms.values.avg).toFixed(2)
        : 'N/A';

    console.log('\n========== BENCHMARK: ASYNC PIPELINE ==========');
    console.log(`Ingress RPS:            ${submitRps.toFixed ? submitRps.toFixed(2) : submitRps}`);
    console.log(`Accepted TPS:           ${acceptedTps.toFixed ? acceptedTps.toFixed(2) : acceptedTps}`);
    console.log(`Submit P95(ms):         ${submitP95}`);
    console.log(`Avg Create Latency(ms): ${avgCreateLatency}`);
    console.log(`Backlog P95:            ${backlogP95}`);
    console.log('===============================================\n');
    return {};
}
