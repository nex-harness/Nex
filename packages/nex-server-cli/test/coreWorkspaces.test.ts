import { afterEach, describe, expect, it } from "vitest";
import {
  IProjectService,
  ServiceCollection,
  type IProjectService as ProjectService,
  type VirtualProject,
} from "@nex/services";
import { buildServerRemoteWorkspaceIdentity } from "@nex/shared";
import { createCoreHttpServer, type CoreHttpServer } from "../src/server-core/http.js";

const openServers: CoreHttpServer[] = [];
afterEach(async () => {
  await Promise.all(openServers.splice(0).map((server) => server.close()));
});

function project(id: string, name: string, rootPath: string, deleted = false): VirtualProject {
  return {
    id,
    hostId: "remote:server:test-server",
    name,
    nameOverridden: false,
    roots: [{ rootPath, position: 0, isPrimary: true }],
    position: 0,
    pinned: false,
    deleted,
    createdAt: 1,
    updatedAt: 1,
  };
}

async function readWorkspaces(projects: VirtualProject[], serverId = "test-server") {
  const services = new ServiceCollection().register(IProjectService, {
    readProjects: async () => projects,
  } as ProjectService);
  const server = await createCoreHttpServer(services, {
    host: "127.0.0.1",
    port: 0,
    serverId,
  });
  openServers.push(server);
  const response = await fetch(`http://127.0.0.1:${server.port}/api/server-info`);
  expect(response.status).toBe(200);
  const body = (await response.json()) as {
    serverId: string;
    workspaces: Array<{
      path: string;
      label?: string;
      workspaceIdentity?: string;
    }>;
  };
  return body;
}

describe("server-core Web bootstrap workspaces", () => {
  it("declares live project roots with stable server identities, not deleted roots", async () => {
    const result = await readWorkspaces([
      project("one", "Nex", "/work/nex"),
      project("deleted", "Removed", "/work/removed", true),
    ]);
    expect(result.serverId).toBe("test-server");
    expect(result.workspaces).toEqual([
      {
        path: "/work/nex",
        label: "Nex",
        workspaceIdentity: buildServerRemoteWorkspaceIdentity({
          serverId: "test-server",
          workspacePath: "/work/nex",
        }),
      },
    ]);
  });

  it("provides a default workspace when no project exists", async () => {
    const result = await readWorkspaces([]);
    expect(result.workspaces).toHaveLength(1);
    expect(result.workspaces[0]?.path.length).toBeGreaterThan(0);
    expect(result.workspaces[0]?.workspaceIdentity).toBe(
      buildServerRemoteWorkspaceIdentity({
        serverId: "test-server",
        workspacePath: result.workspaces[0]!.path,
      }),
    );
  });
});
