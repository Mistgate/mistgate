const { DatabaseSync } = require("node:sqlite");

let sqlite;
let queryStats = null;
let batchDepth = 0;

let holdNextRun = false;
let onNextAwait = null;

function countExecution() {
  if (!queryStats) return;
  queryStats.prepareExecutions++;
  if (batchDepth > 0) queryStats.batchedExecutions++;
  else queryStats.sequentialExecutions++;
}

function resultMeta(changes = 0, lastInsertRowid = 0) {
  return {
    success: true,
    meta: {
      changes: Number(changes),
      last_row_id: Number(lastInsertRowid),
    },
  };
}

// Like Cloudflare's D1, a BLOB comes back as an array of byte values (not a Uint8Array).
function d1Value(value) {
  if (value instanceof Uint8Array) return Array.from(value);
  return typeof value === "bigint" ? Number(value) : value;
}

function d1Row(row) {
  return Object.fromEntries(Object.entries(row).map(([key, value]) => [key, d1Value(value)]));
}

function d1Promise(promise) {
  return {
    then(resolve, reject) {
      const result = promise.then(resolve, reject);
      if (onNextAwait) {
        const callback = onNextAwait;
        onNextAwait = null;
        callback();
      }
      return result;
    },
  };
}

function prepared(query, bound = []) {
  return {
    bind(...args) {
      return prepared(query, args);
    },
    all() {
      countExecution();
      try {
        const statement = sqlite.prepare(query);
        statement.setReadBigInts(true);
        const rows = statement.all(...bound).map(d1Row);
        return d1Promise(Promise.resolve({
          success: true,
          results: rows,
          meta: { changes: 0, last_row_id: 0 },
        }));
      } catch (error) {
        return d1Promise(Promise.reject(error));
      }
    },
    raw(options) {
      countExecution();
      try {
        const statement = sqlite.prepare(query);
        statement.setReadBigInts(true);
        const columnNames = statement.columns().map((column) => column.name);
        statement.setReturnArrays(true);
        const rows = statement.all(...bound).map((row) => row.map(d1Value));
        const result = options?.columnNames ? [columnNames, ...rows] : rows;
        return d1Promise(Promise.resolve(result));
      } catch (error) {
        return d1Promise(Promise.reject(error));
      }
    },
    run() {
      countExecution();
      if (holdNextRun) {
        holdNextRun = false;
        return d1Promise(new Promise(() => {}));
      }
      try {
        const statement = sqlite.prepare(query);
        statement.setReadBigInts(true);
        if (statement.columns().length > 0) {
          const rows = statement.all(...bound).map(d1Row);
          const changes = /^\s*(INSERT|UPDATE|DELETE|REPLACE)\b/i.test(query)
            ? Number(sqlite.prepare("SELECT changes() AS value").get().value)
            : 0;
          const lastInsertRowid = Number(sqlite.prepare("SELECT last_insert_rowid() AS value").get().value);
          return d1Promise(Promise.resolve({
            success: true,
            results: rows,
            meta: { changes, last_row_id: lastInsertRowid },
          }));
        }
        const result = statement.run(...bound);
        return d1Promise(Promise.resolve(resultMeta(result.changes, result.lastInsertRowid)));
      } catch (error) {
        return d1Promise(Promise.reject(error));
      }
    },
    first(column) {
      return this.all().then((result) => {
        const row = result.results[0];
        return column === undefined ? row ?? null : row?.[column] ?? null;
      });
    },
  };
}

function reset() {
  if (sqlite) sqlite.close();
  sqlite = new DatabaseSync(process.env.MISTGATE_BRIDGE_D1_PATH || ":memory:");
  sqlite.exec("PRAGMA foreign_keys = ON");
}

reset();

globalThis.__d1 = {
  prepare(query) {
    return prepared(query);
  },
  async batch(statements) {
    if (queryStats) queryStats.batchCalls++;
    batchDepth++;
    try {
      sqlite.exec("BEGIN");
      const results = [];
      for (const statement of statements) results.push(await statement.run());
      sqlite.exec("COMMIT");
      return results;
    } catch (error) {
      sqlite.exec("ROLLBACK");
      throw error;
    } finally {
      batchDepth--;
    }
  },
  __holdNextRun() {
    holdNextRun = true;
  },
  __onNextAwait(callback) {
    onNextAwait = callback;
  },
  __beginQueryCount(label) {
    if (queryStats) throw new Error("a D1 query count is already active");
    queryStats = { label, prepareExecutions: 0, sequentialExecutions: 0, batchedExecutions: 0, batchCalls: 0 };
  },
  __endQueryCount() {
    if (!queryStats) throw new Error("no D1 query count is active");
    const result = {
      ...queryStats,
      sequentialQueries: queryStats.sequentialExecutions + queryStats.batchCalls,
    };
    queryStats = null;
    return result;
  },
  __reset: reset,
};
