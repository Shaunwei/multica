import { NextRequest, NextResponse } from "next/server";

const remoteApiUrl = process.env.REMOTE_API_URL || "http://localhost:8080";
const cfAccessClientId = process.env.CF_ACCESS_CLIENT_ID;
const cfAccessClientSecret = process.env.CF_ACCESS_CLIENT_SECRET;

function buildUpstreamUrl(path: string, request: NextRequest) {
  const url = new URL(path, remoteApiUrl.endsWith("/") ? remoteApiUrl : `${remoteApiUrl}/`);
  url.search = request.nextUrl.search;
  return url;
}

function copyRequestHeaders(request: NextRequest) {
  const headers = new Headers(request.headers);

  headers.delete("host");
  headers.delete("connection");
  headers.delete("content-length");
  headers.delete("transfer-encoding");
  headers.delete("accept-encoding");

  if (cfAccessClientId && cfAccessClientSecret) {
    headers.set("CF-Access-Client-Id", cfAccessClientId);
    headers.set("CF-Access-Client-Secret", cfAccessClientSecret);
  }

  return headers;
}

function copyResponseHeaders(headers: Headers) {
  const out = new Headers(headers);
  out.delete("content-encoding");
  out.delete("content-length");
  out.delete("transfer-encoding");
  return out;
}

export async function proxyToUpstream(request: NextRequest, upstreamPath: string) {
  const url = buildUpstreamUrl(upstreamPath, request);
  const headers = copyRequestHeaders(request);
  const method = request.method;
  const body = method === "GET" || method === "HEAD" ? undefined : await request.arrayBuffer();

  const response = await fetch(url, {
    method,
    headers,
    body,
    redirect: "manual",
  });

  return new NextResponse(response.body, {
    status: response.status,
    headers: copyResponseHeaders(response.headers),
  });
}
