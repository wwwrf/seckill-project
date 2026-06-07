// Standard benchmark: end-to-end transaction benchmark (TPS)
// Flow: seckill -> poll result -> pay order.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import exec from 'k6/execution';
import { authHeaders, cfgNum, cfgStr, envNum, envStr, loadUsers, pickConfig, pickUser, safeJsonParse } from './lib/common.js';

const cfg = pickConfig('e2e_tps');
const BASE_URL = envStr('BASE_URL', cfgStr(cfg, 'base_url', 'http://127.0.0.1:8080'));
const ACTIVITY_ID = envNum('ACTIVITY_ID', cfgNum(cfg, 'activity_id', 1));
const PRODUCT_ID = envNum('PRODUCT_ID', cfgNum(cfg, 'product_id', 1));

const POLL_INTERVAL_MS = envNum('POLL_INTERVAL_MS', cfgNum(cfg, 'poll_interval_ms', 300));
const POLL_MAX_RETRIES = envNum('POLL_MAX_RETRIES', cfgNum(cfg, 'poll_max_retries', 30));

const usersData = loadUsers('../loadtest/users_token.csv');

function selectUser() {
    if (!usersData || usersData.length === 0) {
        return pickUser(usersData);
    }

    // Use global scenario iteration index to reduce early repeated-order collisions.
    const idx = exec.scenario.iterationInTest % usersData.length;
    return usersData[idx];
}

const txnSuccess = new Counter('txn_success');
const txnFailed = new Counter('txn_failed');
const pollTimeout = new Counter('txn_poll_timeout');
const soldOut = new Counter('txn_sold_out');
const repeated = new Counter('txn_repeated');
const rateLimited = new Counter('txn_rate_limited');

const e2eLatency = new Trend('txn_e2e_latency_ms');

export const options = {
    scenarios: {
        e2e_flow: {
            executor: 'ramping-arrival-rate',
            startRate: envNum('START_RPS', cfgNum(cfg, 'start_rps', 50)),
            timeUnit: '1s',
            preAllocatedVUs: envNum('PRE_VUS', cfgNum(cfg, 'pre_vus', 200)),
            maxVUs: envNum('MAX_VUS', cfgNum(cfg, 'max_vus', 1200)),
            stages: [
                { target: envNum('RPS_STAGE_1', cfgNum(cfg, 'rps_stage_1', 120)), duration: envStr('STAGE_1', cfgStr(cfg, 'stage_1', '30s')) },
                { target: envNum('RPS_STAGE_2', cfgNum(cfg, 'rps_stage_2', 240)), duration: envStr('STAGE_2', cfgStr(cfg, 'stage_2', '40s')) },
                { target: envNum('RPS_STAGE_3', cfgNum(cfg, 'rps_stage_3', 320)), duration: envStr('STAGE_3', cfgStr(cfg, 'stage_3', '40s')) },
                { target: envNum('RPS_STAGE_4', cfgNum(cfg, 'rps_stage_4', 160)), duration: envStr('STAGE_4', cfgStr(cfg, 'stage_4', '20s')) },
            ],
        },
    },
    thresholds: {
        http_req_failed: [`rate<${cfgNum(cfg, 'http_error_rate', 0.05)}`],
        txn_e2e_latency_ms: [`p(95)<${cfgNum(cfg, 'txn_p95_ms', 1500)}`, `p(99)<${cfgNum(cfg, 'txn_p99_ms', 2500)}`],
    },
};

export default function () {
    const start = Date.now();
    const user = selectUser();
    const headers = authHeaders(user.token);

    const seckillRes = http.post(
        `${BASE_URL}/api/v1/seckill`,
        JSON.stringify({ activity_id: ACTIVITY_ID, product_id: PRODUCT_ID }),
        { headers, tags: { step: 'seckill' } },
    );

    if (seckillRes.status !== 200) {
        txnFailed.add(1);
        sleep(0.01);
        return;
    }

    const body = safeJsonParse(seckillRes.body);
    if (!body) {
        txnFailed.add(1);
        sleep(0.01);
        return;
    }

    if (body.code !== 0) {
        if (body.code === 40001) soldOut.add(1);
        else if (body.code === 20002) repeated.add(1);
        else if (body.code === 42900) rateLimited.add(1);
        else txnFailed.add(1);
        sleep(0.02);
        return;
    }

    const orderNo = body.data && body.data.order_no;
    if (!orderNo) {
        txnFailed.add(1);
        return;
    }

    let orderReady = false;
    for (let i = 0; i < POLL_MAX_RETRIES; i++) {
        sleep(POLL_INTERVAL_MS / 1000);
        const pollRes = http.get(
            `${BASE_URL}/api/v1/seckill/result?activity_id=${ACTIVITY_ID}&product_id=${PRODUCT_ID}&order_no=${orderNo}`,
            { headers, tags: { step: 'poll' } },
        );

        if (pollRes.status !== 200) {
            continue;
        }

        const pollBody = safeJsonParse(pollRes.body);
        if (!pollBody || pollBody.code !== 0 || !pollBody.data) {
            continue;
        }

        if (pollBody.data.status === 'SUCCESS') {
            orderReady = true;
            break;
        }

        if (pollBody.data.status === 'FAILED') {
            break;
        }
    }

    if (!orderReady) {
        pollTimeout.add(1);
        txnFailed.add(1);
        return;
    }

    const payRes = http.post(`${BASE_URL}/api/v1/order/${orderNo}/pay`, null, { headers, tags: { step: 'pay' } });
    const payBody = safeJsonParse(payRes.body);
    const ok = check(payRes, {
        'pay http 200': (r) => r.status === 200,
        'pay biz success': () => payBody && payBody.code === 0,
    });

    if (ok) {
        txnSuccess.add(1);
        e2eLatency.add(Date.now() - start);
    } else {
        txnFailed.add(1);
    }

    sleep(0.005);
}

export function handleSummary(data) {
    const m = data.metrics;

    const success = m.txn_success ? m.txn_success.values.count : 0;
    const failed = m.txn_failed ? m.txn_failed.values.count : 0;
    const total = success + failed;
    const failRate = total > 0 ? ((failed * 100) / total).toFixed(2) + '%' : 'N/A';

    const rps = m.http_reqs && m.http_reqs.values ? m.http_reqs.values.rate : 0;
    const tps = m.txn_success && m.txn_success.values ? m.txn_success.values.rate : 0;
    const p95 = m.txn_e2e_latency_ms && m.txn_e2e_latency_ms.values && m.txn_e2e_latency_ms.values['p(95)'] != null
        ? Number(m.txn_e2e_latency_ms.values['p(95)']).toFixed(2)
        : 'N/A';
    const p99 = m.txn_e2e_latency_ms && m.txn_e2e_latency_ms.values && m.txn_e2e_latency_ms.values['p(99)'] != null
        ? Number(m.txn_e2e_latency_ms.values['p(99)']).toFixed(2)
        : 'N/A';

    console.log('\n========== STANDARD BENCHMARK: E2E TRANSACTION ==========');
    console.log(`RPS:            ${rps.toFixed ? rps.toFixed(2) : rps}`);
    console.log(`QPS:            ${rps.toFixed ? rps.toFixed(2) : rps}`);
    console.log(`TPS:            ${tps.toFixed ? tps.toFixed(2) : tps}`);
    console.log(`P95(ms):        ${p95}`);
    console.log(`P99(ms):        ${p99}`);
    console.log(`Txn Fail Rate:  ${failRate}`);
    console.log(`Success:        ${success}`);
    console.log(`Failed:         ${failed}`);
    console.log(`Sold Out:       ${m.txn_sold_out ? m.txn_sold_out.values.count : 0}`);
    console.log(`Repeated:       ${m.txn_repeated ? m.txn_repeated.values.count : 0}`);
    console.log(`Rate Limited:   ${m.txn_rate_limited ? m.txn_rate_limited.values.count : 0}`);
    console.log(`Poll Timeout:   ${m.txn_poll_timeout ? m.txn_poll_timeout.values.count : 0}`);
    console.log('=========================================================\n');

    return {};
}
