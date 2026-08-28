import { greeting } from "./greeting.js";

if (greeting("QA") !== "Hello from the fixture, QA!") {
  throw new Error("greeting output mismatch");
}

console.log("fixture test passed");

