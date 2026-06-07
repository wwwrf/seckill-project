import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import exec from 'k6/execution';
import { authHeaders, cfgNum, cfgStr, envNum, envStr, loadUsers, pickConfig, safeJsonParse } from './lib/common.js';

const cfg = pickConfig('seckill_submit');
const BASE_URL = envStr('BASE_URL', cfgStr(cfg, 'base_url', 'http://127.0.0.1:8080'));
const ACTIVITY_ID = envNum('ACTIVITY_ID', cfgNum(cfg, 'activity_id', 1));
const PRODUCT_ID = envNum('PRODUCT_ID', cfgNum(cfg, 'product_id', 1));
const usersData = loadUsers('../loadtest/users_token.csv');

const okCounter = new Counter('submit_ok');
const errCounter = new Counter('submit_err');
const soldOutCounter = new Counter('submit_sold_out');
const repeatedCounter = new Counter('submit_repeated');
const latency = new Trend('submit_latency_ms');

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
    },
};

export default function () {
    const user = usersData[exec.scenario.iterationInTest % usersData.length];
    const headers = authHeaders(user.token);
    const start = Date.now();
    const res = http.post(`${BASE_URL}/api/v1/seckill`, JSON.stringify({ activity_id: ACTIVITY_ID, product_id: PRODUCT_ID }), { headers });
    latency.add(Date.now() - start);

    const ok = check(res, { 'http 200': (r) => r.status === 200 });
    if (!ok) {
        errCounter.add(1);
        sleep(0.005);
        return;
    }

    const body = safeJsonParse(res.body);
    if (!body) {
        errCounter.add(1);
        return;
    }

    if (body.code === 0) {
        okCounter.add(1);
    } else if (body.code === 40001) {
        soldOutCounter.add(1);
    } else if (body.code === 20002) {
        repeatedCounter.add(1);
    } else {
        errCounter.add(1);
    }

    sleep(0.002);
}

export function handleSummary(data) {
    const m = data.metrics;
    const p95 = m.submit_latency_ms && m.submit_latency_ms.values && m.submit_latency_ms.values['p(95)'] != null
        ? Number(m.submit_latency_ms.values['p(95)']).toFixed(2)
        : 'N/A';
    const p99 = m.submit_latency_ms && m.submit_latency_ms.values && m.submit_latency_ms.values['p(99)'] != null
        ? Number(m.submit_latency_ms.values['p(99)']).toFixed(2)
        : 'N/A';
    console.log('\n========== BENCHMARK: SECKILL SUBMIT ==========');
    console.log(`RPS:            ${m.http_reqs ? m.http_reqs.values.rate.toFixed(2) : 'N/A'}`);
    console.log(`Accepted TPS:   ${m.submit_ok ? m.submit_ok.values.rate.toFixed(2) : 'N/A'}`);
    console.log(`P95(ms):        ${p95}`);
    console.log(`P99(ms):        ${p99}`);
    console.log(`Accepted:       ${m.submit_ok ? m.submit_ok.values.count : 0}`);
    console.log(`Sold Out:       ${m.submit_sold_out ? m.submit_sold_out.values.count : 0}`);
    console.log(`Repeated:       ${m.submit_repeated ? m.submit_repeated.values.count : 0}`);
    console.log(`Error:          ${m.submit_err ? m.submit_err.values.count : 0}`);
    console.log('===============================================\n');
    return {};
}
