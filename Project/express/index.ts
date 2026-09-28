import express from "express";

const app = express();
const PORT = 8000;

app.use(express.json());

app.get("/", (req, res) => {
  res.json({
    message: "Hello Express!",
  });
});

app.get("/users", (req, res) => {
  res.json([
    { id: 1, name: "John" },
    { id: 2, name: "Jane" },
  ]);
});

app.post("/users", (req, res) => {
  const user = req.body;

  res.status(201).json({
    message: "User created",
    user,
  });
});

app.listen(PORT, () => {
  console.log(`Server running at http://localhost:${PORT}`);
});
