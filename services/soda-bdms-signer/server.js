'use strict'

const crypto = require('node:crypto')
const fs = require('node:fs')
const http = require('node:http')
const path = require('node:path')

const bdms = require(path.join(__dirname, 'native', 'bdms.node'))

const host = process.env.SODA_BDMS_HOST || '0.0.0.0'
const port = parsePort(process.env.SODA_BDMS_PORT || '17891')
const token = process.env.SODA_BDMS_TOKEN || ''
const deviceId = requireDigits(process.env.SODA_DEVICE_ID, 'SODA_DEVICE_ID')
const appVersion = process.env.SODA_VERSION || '3.7.0'
const versionCode = process.env.SODA_VERSION_CODE || '30700'
const channel = process.env.SODA_CHANNEL || 'official'
const buildMode = process.env.SODA_BUILD_MODE || 'official'
const timeZone = process.env.SODA_TIMEZONE || 'Asia/Shanghai'
const pcUserAgent = `LunaPC/${appVersion}`
const maxBodyBytes = 64 * 1024
const logFile = 'Z:\\data\\signer.log'

bdms.init({ deviceId })

function parsePort(value) {
  const parsed = Number(value)
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) {
    throw new Error('SODA_BDMS_PORT must be a valid TCP port')
  }
  return parsed
}

function requireDigits(value, name) {
  if (!value || !/^\d{16,20}$/.test(value)) {
    throw new Error(`${name} must be 16-20 digits`)
  }
  return value
}

function sendJSON(response, statusCode, body) {
  const encoded = Buffer.from(JSON.stringify(body))
  response.writeHead(statusCode, {
    'cache-control': 'no-store',
    'content-length': encoded.length,
    'content-type': 'application/json; charset=utf-8',
  })
  response.end(encoded)
}

function writeLog(level, message) {
  try {
    fs.appendFileSync(logFile, `${new Date().toISOString()} ${level} ${message}\n`)
  } catch {
    // Windows Node stdout/stderr handles can be invalid under headless Wine.
    // Logging must never take the signing service down.
  }
}

function authorized(request) {
  if (!token) return true
  const actual = request.headers.authorization || ''
  const expected = `Bearer ${token}`
  const actualBuffer = Buffer.from(actual)
  const expectedBuffer = Buffer.from(expected)
  return actualBuffer.length === expectedBuffer.length
    && crypto.timingSafeEqual(actualBuffer, expectedBuffer)
}

function readJSON(request) {
  return new Promise((resolve, reject) => {
    const chunks = []
    let length = 0
    request.on('data', (chunk) => {
      length += chunk.length
      if (length > maxBodyBytes) {
        reject(Object.assign(new Error('request body too large'), { statusCode: 413 }))
        request.destroy()
        return
      }
      chunks.push(chunk)
    })
    request.on('end', () => {
      try {
        const text = Buffer.concat(chunks).toString('utf8')
        resolve(text ? JSON.parse(text) : {})
      } catch {
        reject(Object.assign(new Error('invalid JSON body'), { statusCode: 400 }))
      }
    })
    request.on('error', reject)
  })
}

function normalizeTarget(rawURL) {
  let target
  try {
    target = new URL(String(rawURL || ''))
  } catch {
    throw Object.assign(new Error('url must be an absolute URL'), { statusCode: 400 })
  }
  if (target.protocol !== 'https:' || target.username || target.password) {
    throw Object.assign(new Error('only credential-free HTTPS URLs are allowed'), { statusCode: 400 })
  }
  const hostname = target.hostname.toLowerCase()
  if (hostname !== 'qishui.com' && !hostname.endsWith('.qishui.com')) {
    throw Object.assign(new Error('target host is not allowed'), { statusCode: 400 })
  }
  return target
}

function applyCommonParams(target) {
  if (target.hostname.toLowerCase() !== 'api.qishui.com' || !target.pathname.startsWith('/luna/pc/')) {
    return false
  }
  const params = {
    aid: '386088',
    app_name: 'luna_pc',
    region: 'cn',
    geo_region: 'cn',
    os_region: 'cn',
    sim_region: '',
    device_id: deviceId,
    fp: deviceId,
    cdid: '',
    iid: deviceId,
    version_name: appVersion,
    version_code: versionCode,
    channel,
    build_mode: buildMode,
    network_carrier: '',
    ac: 'wifi',
    tz_name: timeZone,
    resolution: '',
    device_platform: 'windows',
    device_type: 'Windows',
    os_version: '10',
  }
  for (const [key, value] of Object.entries(params)) {
    target.searchParams.set(key, value)
  }
  return true
}

function normalizeHeaders(input) {
  if (input !== undefined && (input === null || Array.isArray(input) || typeof input !== 'object')) {
    throw Object.assign(new Error('headers must be an object'), { statusCode: 400 })
  }
  const headers = new Map()
  for (const [rawName, rawValue] of Object.entries(input || {})) {
    const name = String(rawName).trim().toLowerCase()
    const value = String(rawValue).trim()
    if (!name || !value) continue
    if (!/^[a-z0-9!#$%&'*+.^_`|~-]+$/.test(name) || /[\r\n]/.test(value)) {
      throw Object.assign(new Error('invalid header name or value'), { statusCode: 400 })
    }
    if (['authorization', 'cookie', 'host', 'x-helios', 'x-medusa'].includes(name)) continue
    headers.set(name, value)
  }
  headers.set('user-agent', headers.get('user-agent') || pcUserAgent)
  return headers
}

function generateSignature(target, headers) {
  const headerLines = []
  for (const [name, value] of headers.entries()) {
    headerLines.push(name, value)
  }
  const raw = bdms.generateHttpSignatureHeaders(target.toString(), headerLines.join('\r\n'))
  const parts = raw.split('\r\n').filter((part) => part.trim())
  if (parts.length < 4 || parts.length % 2 !== 0) {
    throw new Error('BDMS returned an invalid signature')
  }
  const signedHeaders = {}
  for (let index = 0; index < parts.length; index += 2) {
    const name = parts[index]
    const value = parts[index + 1]
    if (/^x-(helios|medusa)$/i.test(name) && value) {
      signedHeaders[name] = value
    }
  }
  const names = Object.keys(signedHeaders).map((name) => name.toLowerCase())
  if (!names.includes('x-helios') || !names.includes('x-medusa')) {
    throw new Error('BDMS did not return both required headers')
  }
  return signedHeaders
}

const server = http.createServer(async (request, response) => {
  try {
    const requestURL = new URL(request.url, `http://127.0.0.1:${port}`)
    if (request.method === 'GET' && requestURL.pathname === '/healthz') {
      sendJSON(response, 200, {
        status: 'ok',
        service: 'soda-bdms-signer',
        version: appVersion,
      })
      return
    }
    if (request.method === 'GET' && requestURL.pathname === '/v1/info') {
      sendJSON(response, 200, {
        service: 'soda-bdms-signer',
        device_id: deviceId,
        app_version: appVersion,
        version_code: versionCode,
        channel,
        build_mode: buildMode,
      })
      return
    }
    if (request.method !== 'POST' || requestURL.pathname !== '/v1/sign') {
      sendJSON(response, 404, { error: 'not found' })
      return
    }
    if (!authorized(request)) {
      sendJSON(response, 401, { error: 'unauthorized' })
      return
    }

    const payload = await readJSON(request)
    const target = normalizeTarget(payload.url)
    const commonParamsAdded = payload.add_common_params === false ? false : applyCommonParams(target)
    const headers = normalizeHeaders(payload.headers)
    const signedHeaders = generateSignature(target, headers)

    sendJSON(response, 200, {
      url: target.toString(),
      headers: signedHeaders,
      common_params_added: commonParamsAdded,
    })
  } catch (error) {
    const statusCode = Number(error && error.statusCode) || 500
    const message = statusCode >= 500 ? 'signature generation failed' : error.message
    if (statusCode >= 500) {
      writeLog('ERROR', error && error.stack ? error.stack : String(error))
    }
    if (!response.headersSent) sendJSON(response, statusCode, { error: message })
    else response.destroy()
  }
})

server.requestTimeout = 10_000
server.headersTimeout = 10_000
server.keepAliveTimeout = 5_000

server.listen(port, host, () => {
  writeLog('INFO', `ready on ${host}:${port} (Soda Music ${appVersion})`)
})

function shutdown(signal) {
  writeLog('INFO', `received ${signal}, shutting down`)
  server.close(() => process.exit(0))
  setTimeout(() => process.exit(1), 5_000).unref()
}

process.on('SIGINT', () => shutdown('SIGINT'))
process.on('SIGTERM', () => shutdown('SIGTERM'))
