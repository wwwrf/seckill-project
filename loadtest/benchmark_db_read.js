// Standard benchmark: bare MySQL read upper bound (QPS)
// Requires custom k6 built with xk6-sql and mysql driver.

import { sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import sql from 'k6/x/sql';
import driver from 'k6/x/sql/driver/mysql';
import { cfgNum, cfgStr, envNum, envStr, pickConfig } from './lib/common.js';

const cfg = pickConfig('db_read');
const MYSQL_DSN = envStr('MYSQL_DSN', cfgStr(cfg, 'mysql_dsn', 'root:@tcp(127.0.0.1:3306)/ecommerce_db'));
const MAX_USER_ID = envNum('MAX_USER_ID', cfgNum(cfg, 'max_user_id', 10000));
const MAX_ORDER_ID = envNum('MAX_ORDER_ID', cfgNum(cfg, 'max_order_id', 200000));

const db = sql.open(driver, MYSQL_DSN);

const readLatency = new Trend('db_read_latency_ms');
const readOk = new Counter('db_read_ok');
const readErr = new Counter('db_read_err');

export const options = {
    scenarios: {
        list_read: {
            executor: 'constant-arrival-rate',
            rate: envNum('RATE_LIST', cfgNum(cfg, 'rate_list', 600)),
            timeUnit: '1s',
            duration: envStr('DURATION_LIST', cfgStr(cfg, 'duration_list', '2m')),
            preAllocatedVUs: envNum('PRE_VUS_LIST', cfgNum(cfg, 'pre_vus_list', 100)),
            maxVUs: envNum('MAX_VUS_LIST', cfgNum(cfg, 'max_vus_list', 400)),
            exec: 'readList',
        },
        detail_read: {
            executor: 'constant-arrival-rate',
            rate: envNum('RATE_DETAIL', cfgNum(cfg, 'rate_detail', 900)),
            timeUnit: '1s',
            duration: envStr('DURATION_DETAIL', cfgStr(cfg, 'duration_detail', '2m')),
            preAllocatedVUs: envNum('PRE_VUS_DETAIL', cfgNum(cfg, 'pre_vus_detail', 120)),
            maxVUs: envNum('MAX_VUS_DETAIL', cfgNum(cfg, 'max_vus_detail', 500)),
            exec: 'readDetail',
        },
    },
    thresholds: {
        db_read_latency_ms: ['p(95)<120', 'p(99)<250'],
    },
};

function randInt(min, max) {
    return Math.floor(Math.random() * (max - min + 1)) + min;
}

export function readList() {
    const userID = randInt(1, MAX_USER_ID);
    const offset = randInt(0, 200);
    const start = Date.now();

    try {
        db.query(
            'SELECT order_no,status,created_at FROM orders WHERE user_id=? ORDER BY id DESC LIMIT 10 OFFSET ?',
            userID,
            offset,
        );
        readOk.add(1);
    } catch (e) {
        readErr.add(1);
    }

    readLatency.add(Date.now() - start);
    sleep(0.002);
}

export function readDetail() {
    const id = randInt(1, MAX_ORDER_ID);
    const start = Date.now();

    try {
        db.query('SELECT id,order_no,user_id,status,total_amount FROM orders WHERE id=?', id);
        readOk.add(1);
    } catch (e) {
        readErr.add(1);
    }

    readLatency.add(Date.now() - start);
    sleep(0.002);
}

export function teardown() {
    db.close();
}

export function handleSummary(data) {
    const m = data.metrics;
    const ok = m.db_read_ok ? m.db_read_ok.values.count : 0;
    const err = m.db_read_err ? m.db_read_err.values.count : 0;
    const total = ok + err;
    const errRate = total > 0 ? ((err * 100) / total).toFixed(2) + '%' : 'N/A';

    const qps = m.iterations && m.iterations.values ? m.iterations.values.rate : 0;

    console.log('\n========== STANDARD BENCHMARK: BARE DB READ ==========');
    console.log(`QPS:            ${qps.toFixed ? qps.toFixed(2) : qps}`);
    console.log('RPS:            N/A (no HTTP layer)');
    console.log('TPS:            N/A (read-only benchmark)');
    console.log(`P95(ms):        ${m.db_read_latency_ms ? m.db_read_latency_ms.values['p(95)'].toFixed(2) : 'N/A'}`);
    console.log(`P99(ms):        ${m.db_read_latency_ms ? m.db_read_latency_ms.values['p(99)'].toFixed(2) : 'N/A'}`);
    console.log(`Error Rate:     ${errRate}`);
    console.log(`Read OK:        ${ok}`);
    console.log(`Read Error:     ${err}`);
    console.log('======================================================\n');

    return {};
}
