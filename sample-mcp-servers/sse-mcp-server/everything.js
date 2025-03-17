"use strict";
var __awaiter = (this && this.__awaiter) || function (thisArg, _arguments, P, generator) {
    function adopt(value) { return value instanceof P ? value : new P(function (resolve) { resolve(value); }); }
    return new (P || (P = Promise))(function (resolve, reject) {
        function fulfilled(value) { try { step(generator.next(value)); } catch (e) { reject(e); } }
        function rejected(value) { try { step(generator["throw"](value)); } catch (e) { reject(e); } }
        function step(result) { result.done ? resolve(result.value) : adopt(result.value).then(fulfilled, rejected); }
        step((generator = generator.apply(thisArg, _arguments || [])).next());
    });
};
var __generator = (this && this.__generator) || function (thisArg, body) {
    var _ = { label: 0, sent: function() { if (t[0] & 1) throw t[1]; return t[1]; }, trys: [], ops: [] }, f, y, t, g;
    return g = { next: verb(0), "throw": verb(1), "return": verb(2) }, typeof Symbol === "function" && (g[Symbol.iterator] = function() { return this; }), g;
    function verb(n) { return function (v) { return step([n, v]); }; }
    function step(op) {
        if (f) throw new TypeError("Generator is already executing.");
        while (g && (g = 0, op[0] && (_ = 0)), _) try {
            if (f = 1, y && (t = op[0] & 2 ? y["return"] : op[0] ? y["throw"] || ((t = y["return"]) && t.call(y), 0) : y.next) && !(t = t.call(y, op[1])).done) return t;
            if (y = 0, t) op = [op[0] & 2, t.value];
            switch (op[0]) {
                case 0: case 1: t = op; break;
                case 4: _.label++; return { value: op[1], done: false };
                case 5: _.label++; y = op[1]; op = [0]; continue;
                case 7: op = _.ops.pop(); _.trys.pop(); continue;
                default:
                    if (!(t = _.trys, t = t.length > 0 && t[t.length - 1]) && (op[0] === 6 || op[0] === 2)) { _ = 0; continue; }
                    if (op[0] === 3 && (!t || (op[1] > t[0] && op[1] < t[3]))) { _.label = op[1]; break; }
                    if (op[0] === 6 && _.label < t[1]) { _.label = t[1]; t = op; break; }
                    if (t && _.label < t[2]) { _.label = t[2]; _.ops.push(op); break; }
                    if (t[2]) _.ops.pop();
                    _.trys.pop(); continue;
            }
            op = body.call(thisArg, _);
        } catch (e) { op = [6, e]; y = 0; } finally { f = t = 0; }
        if (op[0] & 5) throw op[1]; return { value: op[0] ? op[1] : void 0, done: true };
    }
};
var __spreadArray = (this && this.__spreadArray) || function (to, from, pack) {
    if (pack || arguments.length === 2) for (var i = 0, l = from.length, ar; i < l; i++) {
        if (ar || !(i in from)) {
            if (!ar) ar = Array.prototype.slice.call(from, 0, i);
            ar[i] = from[i];
        }
    }
    return to.concat(ar || Array.prototype.slice.call(from));
};
Object.defineProperty(exports, "__esModule", { value: true });
exports.createServer = void 0;
var index_js_1 = require("@modelcontextprotocol/sdk/server/index.js");
var types_js_1 = require("@modelcontextprotocol/sdk/types.js");
var zod_1 = require("zod");
var zod_to_json_schema_1 = require("zod-to-json-schema");
var ToolInputSchema = types_js_1.ToolSchema.shape.inputSchema;
/* Input schemas for tools implemented in this server */
var EchoSchema = zod_1.z.object({
    message: zod_1.z.string().describe("Message to echo"),
});
var AddSchema = zod_1.z.object({
    a: zod_1.z.number().describe("First number"),
    b: zod_1.z.number().describe("Second number"),
});
var LongRunningOperationSchema = zod_1.z.object({
    duration: zod_1.z
        .number()
        .default(10)
        .describe("Duration of the operation in seconds"),
    steps: zod_1.z.number().default(5).describe("Number of steps in the operation"),
});
var PrintEnvSchema = zod_1.z.object({});
var SampleLLMSchema = zod_1.z.object({
    prompt: zod_1.z.string().describe("The prompt to send to the LLM"),
    maxTokens: zod_1.z
        .number()
        .default(100)
        .describe("Maximum number of tokens to generate"),
});
// Example completion values
var EXAMPLE_COMPLETIONS = {
    style: ["casual", "formal", "technical", "friendly"],
    temperature: ["0", "0.5", "0.7", "1.0"],
    resourceId: ["1", "2", "3", "4", "5"],
};
var GetTinyImageSchema = zod_1.z.object({});
var AnnotatedMessageSchema = zod_1.z.object({
    messageType: zod_1.z.enum(["error", "success", "debug"])
        .describe("Type of message to demonstrate different annotation patterns"),
    includeImage: zod_1.z.boolean().default(false)
        .describe("Whether to include an example image")
});
var ToolName;
(function (ToolName) {
    ToolName["ECHO"] = "echo";
    ToolName["ADD"] = "add";
    ToolName["LONG_RUNNING_OPERATION"] = "longRunningOperation";
    ToolName["PRINT_ENV"] = "printEnv";
    ToolName["SAMPLE_LLM"] = "sampleLLM";
    ToolName["GET_TINY_IMAGE"] = "getTinyImage";
    ToolName["ANNOTATED_MESSAGE"] = "annotatedMessage";
})(ToolName || (ToolName = {}));
var PromptName;
(function (PromptName) {
    PromptName["SIMPLE"] = "simple_prompt";
    PromptName["COMPLEX"] = "complex_prompt";
})(PromptName || (PromptName = {}));
var createServer = function () {
    var server = new index_js_1.Server({
        name: "example-servers/everything",
        version: "1.0.0",
    }, {
        capabilities: {
            prompts: {},
            resources: { subscribe: true },
            tools: {},
            logging: {},
        },
    });
    var subscriptions = new Set();
    var updateInterval;
    // Set up update interval for subscribed resources
    updateInterval = setInterval(function () {
        for (var _i = 0, subscriptions_1 = subscriptions; _i < subscriptions_1.length; _i++) {
            var uri = subscriptions_1[_i];
            server.notification({
                method: "notifications/resources/updated",
                params: { uri: uri },
            });
        }
    }, 5000);
    // Helper method to request sampling from client
    var requestSampling = function (context_1, uri_1) {
        var args_1 = [];
        for (var _i = 2; _i < arguments.length; _i++) {
            args_1[_i - 2] = arguments[_i];
        }
        return __awaiter(void 0, __spreadArray([context_1, uri_1], args_1, true), void 0, function (context, uri, maxTokens) {
            var request;
            if (maxTokens === void 0) { maxTokens = 100; }
            return __generator(this, function (_a) {
                switch (_a.label) {
                    case 0:
                        request = {
                            method: "sampling/createMessage",
                            params: {
                                messages: [
                                    {
                                        role: "user",
                                        content: {
                                            type: "text",
                                            text: "Resource ".concat(uri, " context: ").concat(context),
                                        },
                                    },
                                ],
                                systemPrompt: "You are a helpful test server.",
                                maxTokens: maxTokens,
                                temperature: 0.7,
                                includeContext: "thisServer",
                            },
                        };
                        return [4 /*yield*/, server.request(request, types_js_1.CreateMessageResultSchema)];
                    case 1: return [2 /*return*/, _a.sent()];
                }
            });
        });
    };
    var ALL_RESOURCES = Array.from({ length: 100 }, function (_, i) {
        var uri = "test://static/resource/".concat(i + 1);
        if (i % 2 === 0) {
            return {
                uri: uri,
                name: "Resource ".concat(i + 1),
                mimeType: "text/plain",
                text: "Resource ".concat(i + 1, ": This is a plaintext resource"),
            };
        }
        else {
            var buffer = Buffer.from("Resource ".concat(i + 1, ": This is a base64 blob"));
            return {
                uri: uri,
                name: "Resource ".concat(i + 1),
                mimeType: "application/octet-stream",
                blob: buffer.toString("base64"),
            };
        }
    });
    var PAGE_SIZE = 10;
    server.setRequestHandler(types_js_1.ListResourcesRequestSchema, function (request) { return __awaiter(void 0, void 0, void 0, function () {
        var cursor, startIndex, decodedCursor, endIndex, resources, nextCursor;
        var _a;
        return __generator(this, function (_b) {
            cursor = (_a = request.params) === null || _a === void 0 ? void 0 : _a.cursor;
            startIndex = 0;
            if (cursor) {
                decodedCursor = parseInt(atob(cursor), 10);
                if (!isNaN(decodedCursor)) {
                    startIndex = decodedCursor;
                }
            }
            endIndex = Math.min(startIndex + PAGE_SIZE, ALL_RESOURCES.length);
            resources = ALL_RESOURCES.slice(startIndex, endIndex);
            if (endIndex < ALL_RESOURCES.length) {
                nextCursor = btoa(endIndex.toString());
            }
            return [2 /*return*/, {
                    resources: resources,
                    nextCursor: nextCursor,
                }];
        });
    }); });
    server.setRequestHandler(types_js_1.ListResourceTemplatesRequestSchema, function () { return __awaiter(void 0, void 0, void 0, function () {
        return __generator(this, function (_a) {
            return [2 /*return*/, {
                    resourceTemplates: [
                        {
                            uriTemplate: "test://static/resource/{id}",
                            name: "Static Resource",
                            description: "A static resource with a numeric ID",
                        },
                    ],
                }];
        });
    }); });
    server.setRequestHandler(types_js_1.ReadResourceRequestSchema, function (request) { return __awaiter(void 0, void 0, void 0, function () {
        var uri, index, resource;
        var _a;
        return __generator(this, function (_b) {
            uri = request.params.uri;
            if (uri.startsWith("test://static/resource/")) {
                index = parseInt((_a = uri.split("/").pop()) !== null && _a !== void 0 ? _a : "", 10) - 1;
                if (index >= 0 && index < ALL_RESOURCES.length) {
                    resource = ALL_RESOURCES[index];
                    return [2 /*return*/, {
                            contents: [resource],
                        }];
                }
            }
            throw new Error("Unknown resource: ".concat(uri));
        });
    }); });
    server.setRequestHandler(types_js_1.SubscribeRequestSchema, function (request) { return __awaiter(void 0, void 0, void 0, function () {
        var uri;
        return __generator(this, function (_a) {
            switch (_a.label) {
                case 0:
                    uri = request.params.uri;
                    subscriptions.add(uri);
                    // Request sampling from client when someone subscribes
                    return [4 /*yield*/, requestSampling("A new subscription was started", uri)];
                case 1:
                    // Request sampling from client when someone subscribes
                    _a.sent();
                    return [2 /*return*/, {}];
            }
        });
    }); });
    server.setRequestHandler(types_js_1.UnsubscribeRequestSchema, function (request) { return __awaiter(void 0, void 0, void 0, function () {
        return __generator(this, function (_a) {
            subscriptions.delete(request.params.uri);
            return [2 /*return*/, {}];
        });
    }); });
    server.setRequestHandler(types_js_1.ListPromptsRequestSchema, function () { return __awaiter(void 0, void 0, void 0, function () {
        return __generator(this, function (_a) {
            return [2 /*return*/, {
                    prompts: [
                        {
                            name: PromptName.SIMPLE,
                            description: "A prompt without arguments",
                        },
                        {
                            name: PromptName.COMPLEX,
                            description: "A prompt with arguments",
                            arguments: [
                                {
                                    name: "temperature",
                                    description: "Temperature setting",
                                    required: true,
                                },
                                {
                                    name: "style",
                                    description: "Output style",
                                    required: false,
                                },
                            ],
                        },
                    ],
                }];
        });
    }); });
    server.setRequestHandler(types_js_1.GetPromptRequestSchema, function (request) { return __awaiter(void 0, void 0, void 0, function () {
        var _a, name, args;
        return __generator(this, function (_b) {
            _a = request.params, name = _a.name, args = _a.arguments;
            if (name === PromptName.SIMPLE) {
                return [2 /*return*/, {
                        messages: [
                            {
                                role: "user",
                                content: {
                                    type: "text",
                                    text: "This is a simple prompt without arguments.",
                                },
                            },
                        ],
                    }];
            }
            if (name === PromptName.COMPLEX) {
                return [2 /*return*/, {
                        messages: [
                            {
                                role: "user",
                                content: {
                                    type: "text",
                                    text: "This is a complex prompt with arguments: temperature=".concat(args === null || args === void 0 ? void 0 : args.temperature, ", style=").concat(args === null || args === void 0 ? void 0 : args.style),
                                },
                            },
                            {
                                role: "assistant",
                                content: {
                                    type: "text",
                                    text: "I understand. You've provided a complex prompt with temperature and style arguments. How would you like me to proceed?",
                                },
                            },
                            {
                                role: "user",
                                content: {
                                    type: "image",
                                    data: MCP_TINY_IMAGE,
                                    mimeType: "image/png",
                                },
                            },
                        ],
                    }];
            }
            throw new Error("Unknown prompt: ".concat(name));
        });
    }); });
    server.setRequestHandler(types_js_1.ListToolsRequestSchema, function () { return __awaiter(void 0, void 0, void 0, function () {
        var tools;
        return __generator(this, function (_a) {
            tools = [
                {
                    name: ToolName.ECHO,
                    description: "Echoes back the input",
                    inputSchema: (0, zod_to_json_schema_1.zodToJsonSchema)(EchoSchema),
                },
                {
                    name: ToolName.ADD,
                    description: "Adds two numbers",
                    inputSchema: (0, zod_to_json_schema_1.zodToJsonSchema)(AddSchema),
                },
                {
                    name: ToolName.PRINT_ENV,
                    description: "Prints all environment variables, helpful for debugging MCP server configuration",
                    inputSchema: (0, zod_to_json_schema_1.zodToJsonSchema)(PrintEnvSchema),
                },
                {
                    name: ToolName.LONG_RUNNING_OPERATION,
                    description: "Demonstrates a long running operation with progress updates",
                    inputSchema: (0, zod_to_json_schema_1.zodToJsonSchema)(LongRunningOperationSchema),
                },
                {
                    name: ToolName.SAMPLE_LLM,
                    description: "Samples from an LLM using MCP's sampling feature",
                    inputSchema: (0, zod_to_json_schema_1.zodToJsonSchema)(SampleLLMSchema),
                },
                {
                    name: ToolName.GET_TINY_IMAGE,
                    description: "Returns the MCP_TINY_IMAGE",
                    inputSchema: (0, zod_to_json_schema_1.zodToJsonSchema)(GetTinyImageSchema),
                },
                {
                    name: ToolName.ANNOTATED_MESSAGE,
                    description: "Demonstrates how annotations can be used to provide metadata about content",
                    inputSchema: (0, zod_to_json_schema_1.zodToJsonSchema)(AnnotatedMessageSchema),
                },
            ];
            return [2 /*return*/, { tools: tools }];
        });
    }); });
    server.setRequestHandler(types_js_1.CallToolRequestSchema, function (request) { return __awaiter(void 0, void 0, void 0, function () {
        var _a, name, args, validatedArgs, validatedArgs, sum, validatedArgs, duration, steps, stepDuration_1, progressToken, i, validatedArgs, prompt_1, maxTokens, result, _b, messageType, includeImage, content;
        var _c;
        return __generator(this, function (_d) {
            switch (_d.label) {
                case 0:
                    _a = request.params, name = _a.name, args = _a.arguments;
                    if (name === ToolName.ECHO) {
                        validatedArgs = EchoSchema.parse(args);
                        return [2 /*return*/, {
                                content: [{ type: "text", text: "Echo: ".concat(validatedArgs.message) }],
                            }];
                    }
                    if (name === ToolName.ADD) {
                        validatedArgs = AddSchema.parse(args);
                        sum = validatedArgs.a + validatedArgs.b;
                        return [2 /*return*/, {
                                content: [
                                    {
                                        type: "text",
                                        text: "The sum of ".concat(validatedArgs.a, " and ").concat(validatedArgs.b, " is ").concat(sum, "."),
                                    },
                                ],
                            }];
                    }
                    if (!(name === ToolName.LONG_RUNNING_OPERATION)) return [3 /*break*/, 6];
                    validatedArgs = LongRunningOperationSchema.parse(args);
                    duration = validatedArgs.duration, steps = validatedArgs.steps;
                    stepDuration_1 = duration / steps;
                    progressToken = (_c = request.params._meta) === null || _c === void 0 ? void 0 : _c.progressToken;
                    i = 1;
                    _d.label = 1;
                case 1:
                    if (!(i < steps + 1)) return [3 /*break*/, 5];
                    return [4 /*yield*/, new Promise(function (resolve) {
                            return setTimeout(resolve, stepDuration_1 * 1000);
                        })];
                case 2:
                    _d.sent();
                    if (!(progressToken !== undefined)) return [3 /*break*/, 4];
                    return [4 /*yield*/, server.notification({
                            method: "notifications/progress",
                            params: {
                                progress: i,
                                total: steps,
                                progressToken: progressToken,
                            },
                        })];
                case 3:
                    _d.sent();
                    _d.label = 4;
                case 4:
                    i++;
                    return [3 /*break*/, 1];
                case 5: return [2 /*return*/, {
                        content: [
                            {
                                type: "text",
                                text: "Long running operation completed. Duration: ".concat(duration, " seconds, Steps: ").concat(steps, "."),
                            },
                        ],
                    }];
                case 6:
                    if (name === ToolName.PRINT_ENV) {
                        return [2 /*return*/, {
                                content: [
                                    {
                                        type: "text",
                                        text: JSON.stringify(process.env, null, 2),
                                    },
                                ],
                            }];
                    }
                    if (!(name === ToolName.SAMPLE_LLM)) return [3 /*break*/, 8];
                    validatedArgs = SampleLLMSchema.parse(args);
                    prompt_1 = validatedArgs.prompt, maxTokens = validatedArgs.maxTokens;
                    return [4 /*yield*/, requestSampling(prompt_1, ToolName.SAMPLE_LLM, maxTokens)];
                case 7:
                    result = _d.sent();
                    return [2 /*return*/, {
                            content: [{ type: "text", text: "LLM sampling result: ".concat(result.content.text) }],
                        }];
                case 8:
                    if (name === ToolName.GET_TINY_IMAGE) {
                        GetTinyImageSchema.parse(args);
                        return [2 /*return*/, {
                                content: [
                                    {
                                        type: "text",
                                        text: "This is a tiny image:",
                                    },
                                    {
                                        type: "image",
                                        data: MCP_TINY_IMAGE,
                                        mimeType: "image/png",
                                    },
                                    {
                                        type: "text",
                                        text: "The image above is the MCP tiny image.",
                                    },
                                ],
                            }];
                    }
                    if (name === ToolName.ANNOTATED_MESSAGE) {
                        _b = AnnotatedMessageSchema.parse(args), messageType = _b.messageType, includeImage = _b.includeImage;
                        content = [];
                        // Main message with different priorities/audiences based on type
                        if (messageType === "error") {
                            content.push({
                                type: "text",
                                text: "Error: Operation failed",
                                annotations: {
                                    priority: 1.0, // Errors are highest priority
                                    audience: ["user", "assistant"] // Both need to know about errors
                                }
                            });
                        }
                        else if (messageType === "success") {
                            content.push({
                                type: "text",
                                text: "Operation completed successfully",
                                annotations: {
                                    priority: 0.7, // Success messages are important but not critical
                                    audience: ["user"] // Success mainly for user consumption
                                }
                            });
                        }
                        else if (messageType === "debug") {
                            content.push({
                                type: "text",
                                text: "Debug: Cache hit ratio 0.95, latency 150ms",
                                annotations: {
                                    priority: 0.3, // Debug info is low priority
                                    audience: ["assistant"] // Technical details for assistant
                                }
                            });
                        }
                        // Optional image with its own annotations
                        if (includeImage) {
                            content.push({
                                type: "image",
                                data: MCP_TINY_IMAGE,
                                mimeType: "image/png",
                                annotations: {
                                    priority: 0.5,
                                    audience: ["user"] // Images primarily for user visualization
                                }
                            });
                        }
                        return [2 /*return*/, { content: content }];
                    }
                    throw new Error("Unknown tool: ".concat(name));
            }
        });
    }); });
    server.setRequestHandler(types_js_1.CompleteRequestSchema, function (request) { return __awaiter(void 0, void 0, void 0, function () {
        var _a, ref, argument, resourceId, values, completions, values;
        return __generator(this, function (_b) {
            _a = request.params, ref = _a.ref, argument = _a.argument;
            if (ref.type === "ref/resource") {
                resourceId = ref.uri.split("/").pop();
                if (!resourceId)
                    return [2 /*return*/, { completion: { values: [] } }];
                values = EXAMPLE_COMPLETIONS.resourceId.filter(function (id) {
                    return id.startsWith(argument.value);
                });
                return [2 /*return*/, { completion: { values: values, hasMore: false, total: values.length } }];
            }
            if (ref.type === "ref/prompt") {
                completions = EXAMPLE_COMPLETIONS[argument.name];
                if (!completions)
                    return [2 /*return*/, { completion: { values: [] } }];
                values = completions.filter(function (value) {
                    return value.startsWith(argument.value);
                });
                return [2 /*return*/, { completion: { values: values, hasMore: false, total: values.length } }];
            }
            throw new Error("Unknown reference type");
        });
    }); });
    server.setRequestHandler(types_js_1.SetLevelRequestSchema, function (request) { return __awaiter(void 0, void 0, void 0, function () {
        var level;
        return __generator(this, function (_a) {
            switch (_a.label) {
                case 0:
                    level = request.params.level;
                    // Demonstrate different log levels
                    return [4 /*yield*/, server.notification({
                            method: "notifications/message",
                            params: {
                                level: "debug",
                                logger: "test-server",
                                data: "Logging level set to: ".concat(level),
                            },
                        })];
                case 1:
                    // Demonstrate different log levels
                    _a.sent();
                    return [2 /*return*/, {}];
            }
        });
    }); });
    var cleanup = function () { return __awaiter(void 0, void 0, void 0, function () {
        return __generator(this, function (_a) {
            if (updateInterval) {
                clearInterval(updateInterval);
            }
            return [2 /*return*/];
        });
    }); };
    return { server: server, cleanup: cleanup };
};
exports.createServer = createServer;
var MCP_TINY_IMAGE = "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";
