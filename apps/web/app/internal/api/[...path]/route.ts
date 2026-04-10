import { NextRequest } from "next/server";
import { proxyToUpstream } from "@/shared/server/upstream-proxy";

async function handler(
  request: NextRequest,
  context: { params: Promise<{ path?: string[] }> }
) {
  const { path = [] } = await context.params;
  return proxyToUpstream(request, `/api/${path.join("/")}`);
}

export const GET = handler;
export const POST = handler;
export const PUT = handler;
export const PATCH = handler;
export const DELETE = handler;
export const OPTIONS = handler;
export const HEAD = handler;
