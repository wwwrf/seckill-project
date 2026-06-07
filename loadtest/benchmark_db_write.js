import { sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import sql from 'k6/x/sql';
import driver from 'k6/x/sql/driver/mysql';
import { cfgNum, cfgStr, envNum, envStr, pickConfig } from './lib/common.js';

const cfg = pickConfig('db_write');
const MYSQL_DSN = envStr('MYSQL_DSN', cfgStr(cfg, 'mysql_dsn', 'root:root123456@tcp(127.0.0.1:3307)/ecommerce_db'));
const ACTIVITY_ID = envNum('ACTIVITY_ID', cfgNum(cfg, 'activity_id', 900001));
const PRODUCT_ID = envNum('PRODUCT_ID', cfgNum(cfg, 'product_id', 900001));
const START_USER_ID = envNum('START_USER_ID', cfgNum(cfg, 'start_user_id', 2000000));

const db = sql.open(driver, MYSQL_DSN);

const writeLatency = new Trend('db_write_latency_ms');
const writeOk = new Counter('db_write_ok');
const writeErr = new Counter('db_write_err');

export const options = {
    scenarios: {
        db_write: {
            executor: 'constant-arrival-rate',
            rate: envNum('TARGET_TPS', cfgNum(cfg, 'target_tps', 300)),
            timeUnit: '1s',
            duration: envStr('DURATION', cfgStr(cfg, 'duration', '1m')),
            preAllocatedVUs: envNum('PRE_VUS', cfgNum(cfg, 'pre_vus', 100)),
            maxVUs: envNum('MAX_VUS', cfgNum(cfg, 'max_vus', 600)),
        },
    },
    thresholds: {
        db_write_latency_ms: ['p(95)<200', 'p(99)<400'],
    },
};

export default function () {
    const userID = START_USER_ID + __ITER + 1;
    const orderNo = `bench_dbw_${Date.now()}_${__VU}_${__ITER}`;
    const total = 199900;
    const start = Date.now();

    try {
        db.exec(
            'INSERT INTO orders (order_no, user_id, activity_id, total_amount, pay_amount, status, order_type, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 0, 1, NOW(), NOW())',
            orderNo, userID, ACTIVITY_ID, total, total,
        );
        db.exec(
            'INSERT INTO order_items (order_id, product_id, snapshot_title, snapshot_price, quantity, total_price, created_at, updated_at) SELECT id, ?, ?, ?, 1, ?, NOW(), NOW() FROM orders WHERE order_no=?',
            PRODUCT_ID, 'bench_db_write_item', total, total, orderNo,
        );
        writeOk.add(1);
    } catch (e) {
        writeErr.add(1);
    }

    writeLatency.add(Date.now() - start);
    sleep(0.001);
}

export function teardown() {
    db.close();
}

export function handleSummary(data) {
    const m = data.metrics;
    const ok = m.db_write_ok ? m.db_write_ok.values.count : 0;
    const err = m.db_write_err ? m.db_write_err.values.count : 0;
    const tps = m.db_write_ok && m.db_write_ok.values ? m.db_write_ok.values.rate : 0;
    const errRate = ok + err > 0 ? ((err * 100) / (ok + err)).toFixed(2) + '%' : 'N/A';

    console.log('\n========== BENCHMARK: BARE DB WRITE ==========');
    console.log(`TPS:            ${tps.toFixed ? tps.toFixed(2) : tps}`);
    console.log(`P95(ms):        ${m.db_write_latency_ms ? m.db_write_latency_ms.values['p(95)'].toFixed(2) : 'N/A'}`);
    console.log(`P99(ms):        ${m.db_write_latency_ms ? m.db_write_latency_ms.values['p(99)'].toFixed(2) : 'N/A'}`);
    console.log(`Error Rate:     ${errRate}`);
    console.log(`Write OK:       ${ok}`);
    console.log(`Write Error:    ${err}`);
    console.log('==============================================\n');

    return {};
}
