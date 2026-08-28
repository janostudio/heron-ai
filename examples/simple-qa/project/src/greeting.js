import { appConfig } from "./config.js";

export function greeting(name = "Agent") {
  return `${appConfig.greeting}, ${name}!`;
}

